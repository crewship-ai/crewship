package devcontainer

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// agentAccountByImage memoises, per immutable image ID, whether the image's
// /etc/passwd has an entry for the agent UID. The answer for an image ID can
// never change, so each image is read once per server lifetime and every
// later cache hit costs a map lookup.
var agentAccountByImage sync.Map // image ID -> bool

const passwdReadLimit = 1 << 20

// cachedImageLacksAgentAccount reports true only when it positively read the
// cached image's /etc/passwd and found no entry for UID 1001. Images built
// before ensureAgentAccountScript existed can lack that entry, which is the
// `sudo: you do not exist in the passwd database` failure; such an image is
// rebuilt once instead of being reused forever. Any doubt — no copy support,
// an unreadable archive, a timeout — keeps the cache hit: this check must
// never turn a fast reprovision into a slow one on a guess.
func (p *Provisioner) cachedImageLacksAgentAccount(ctx context.Context, imageID string) bool {
	if v, ok := agentAccountByImage.Load(imageID); ok {
		return !v.(bool)
	}
	copier, ok := p.docker.(imageFileCopier)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	created, err := p.docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: imageID, User: "1001:1001", Entrypoint: []string{"/bin/true"}, Labels: map[string]string{TempContainerLabelKey: TempContainerLabelValue}},
		HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}},
		Name:       tempContainerName(),
	})
	if err != nil {
		return false
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := p.docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			p.logger.Warn("remove passwd inspection container", "container_id", created.ID, "error", err)
		}
	}()
	copied, err := copier.CopyFromContainer(ctx, created.ID, client.CopyFromContainerOptions{SourcePath: "/etc/passwd"})
	if err != nil {
		return false
	}
	defer copied.Content.Close()
	has, err := passwdArchiveHasUID(copied.Content, agentUID)
	if err != nil {
		return false
	}
	agentAccountByImage.Store(imageID, has)
	return !has
}

const agentUID = "1001"

// passwdArchiveHasUID reads the single-file tar Docker returns for
// /etc/passwd and reports whether any entry has the given UID. The file is
// parsed, never extracted, and bounded in size.
func passwdArchiveHasUID(r io.Reader, uid string) (bool, error) {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false, errors.New("passwd not found in archive")
		}
		if err != nil {
			return false, err
		}
		if h.Typeflag != tar.TypeReg || path.Base(path.Clean(h.Name)) != "passwd" {
			continue
		}
		if h.Size > passwdReadLimit {
			return false, errors.New("passwd exceeds limit")
		}
		sc := bufio.NewScanner(io.LimitReader(tr, passwdReadLimit))
		for sc.Scan() {
			fields := strings.Split(sc.Text(), ":")
			if len(fields) >= 3 && fields[2] == uid {
				return true, nil
			}
		}
		return false, sc.Err()
	}
}
