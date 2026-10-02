package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/moby/moby/api/types/container"
	"net/netip"
	"os"
	"strings"
	"sync"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// Per-crew networks (#2240, first stage).
//
// Every crew container used to join one instance-wide bridge, so crews could
// reach each other by IP and two crews' `postgres` service aliases collided.
// A crew listed in Config.CrewNetworkCrews gets its own bridge instead; its
// services join it under their bare names. Crews not listed are unchanged.
//
// Addressing: Docker's DEFAULT address pools (not a hard Docker limit) hold
// roughly 31 bridges per daemon and every crewship instance already takes a
// /16, so a crew network
// gets an explicit /27 from CrewNetworkPool. The free subnet is chosen against
// EVERY network on the daemon (other instances share it) and the host's IPv4
// routes. IPv6 is off on crew networks.

const (
	defaultCrewNetworkPool = "10.231.0.0/16"
	crewNetworkPrefixBits  = 27
	// crewNetworkPoolMinBits caps the pool at a /16: 2048 crew subnets, and
	// a bounded candidate list (a /0 would be 134 million).
	crewNetworkPoolMinBits = 16
	crewNetworkKind        = resourcelifecycle.NetworkKind
	crewKindLabelValueNet  = crewNetworkKind
)

// crewNetworkAlloc serialises subnet allocation within this process. Two
// instances racing on one daemon are resolved by Docker refusing the
// overlapping create; ensureCrewNetwork then moves to the next candidate.
var crewNetworkAlloc sync.Mutex

// errCrewNetworkPool marks a pool that cannot serve: unparsable, too small,
// overlapping a host route, or exhausted.
var errCrewNetworkPool = errors.New("crew network pool unusable")

// crewNetworkWanted reports whether the crew is on the per-crew network list.
func (p *Provider) crewNetworkWanted(id, slug string) bool {
	for _, c := range p.cfg.CrewNetworkCrews {
		if c != "" && (c == id || c == slug) {
			return true
		}
	}
	return false
}

// crewNetworkName is the per-crew network of crew id on this instance. The
// instance network name is the prefix, so two instances on one daemon never
// share a name.
func (p *Provider) crewNetworkName(id string) string {
	base := p.cfg.Network
	if base == "" {
		base = "crewship"
	}
	return base + "-crew-" + id
}

// crewNetworkFor is THE network a crew's runtime and service containers join:
// its own when listed, the instance network otherwise. Every crew-scoped
// network decision goes through here.
func (p *Provider) crewNetworkFor(id, slug string) string {
	if p.crewNetworkWanted(id, slug) {
		return p.crewNetworkName(id)
	}
	return p.cfg.Network
}

// ensureCrewNetwork makes sure the network crewNetworkFor names exists and
// returns its name ("" when the instance runs without a network).
func (p *Provider) ensureCrewNetwork(ctx context.Context, id, slug string) (string, error) {
	if !p.crewNetworkWanted(id, slug) {
		if p.cfg.Network == "" {
			return "", nil
		}
		return p.cfg.Network, p.ensureNetwork(ctx, p.cfg.Network)
	}
	return p.ensureCrewNetworkNamed(ctx, id, slug)
}

// ensureCrewNetworkNamed creates (or verifies) crew id's own network.
func (p *Provider) ensureCrewNetworkNamed(ctx context.Context, id, slug string) (string, error) {
	name := p.crewNetworkName(id)
	crewNetworkAlloc.Lock()
	defer crewNetworkAlloc.Unlock()

	nets, err := p.client.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return "", fmt.Errorf("crew network: list networks: %w", err)
	}
	var used []netip.Prefix
	for _, n := range nets.Items {
		if n.Name == name {
			if n.Labels[crewKindLabel] != crewKindLabelValueNet || n.Labels[crewCrewIDLabel] != id {
				return "", fmt.Errorf("crew network: %q exists but is not crewship's network for crew %s", name, id)
			}
			return name, nil
		}
		for _, c := range n.IPAM.Config {
			if c.Subnet.IsValid() {
				used = append(used, c.Subnet)
			}
		}
	}
	pool, err := p.crewNetworkPool()
	if err != nil {
		return "", err
	}
	// The route check reads THIS process's routing table, which is the
	// daemon host's only when crewshipd runs natively next to a local
	// daemon. Inside a container, or against a remote daemon, it would miss
	// the host's LAN/VPN routes: require an explicit pool there instead of
	// trusting a check that cannot see.
	if p.hostRoutesVisible() {
		routes, err := hostIPv4Routes()
		if err != nil {
			return "", fmt.Errorf("crew network: read host routes: %w", err)
		}
		if r, bad := foreignRouteInPool(pool, routes, used); bad {
			return "", fmt.Errorf("%w: %s overlaps host route %s; set container.crew_network_pool", errCrewNetworkPool, pool, r)
		}
	} else if p.cfg.CrewNetworkPool == "" {
		return "", fmt.Errorf("%w: crewship cannot see the Docker host's routes (it runs in a container or against a remote daemon); set container.crew_network_pool to a range free on that host", errCrewNetworkPool)
	}
	enableIPv6 := false
	for _, subnet := range candidateSubnets(pool, crewNetworkPrefixBits) {
		if overlapsAny(subnet, used) {
			continue
		}
		gw := subnet.Addr().Next()
		_, err := p.client.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver:     "bridge",
			EnableIPv6: &enableIPv6,
			IPAM: &network.IPAM{
				Driver: "default",
				Config: []network.IPAMConfig{{Subnet: subnet, Gateway: gw}},
			},
			Labels: resourcelifecycle.WithInstanceLabel(map[string]string{
				"managed-by":    "crewship",
				crewKindLabel:   crewKindLabelValueNet,
				crewCrewIDLabel: id,
				crewCrewLabel:   slug,
			}, p.cfg.InstanceID),
		})
		if err == nil {
			p.logger.Info("created crew network", "network", name, "subnet", subnet.String(), "crew_id", id)
			return name, nil
		}
		// Another instance on the same daemon took this subnet between our
		// list and our create: try the next one.
		if strings.Contains(strings.ToLower(err.Error()), "overlap") {
			used = append(used, subnet)
			continue
		}
		return "", fmt.Errorf("crew network: create %s: %w", name, err)
	}
	return "", fmt.Errorf("%w: no free /%d left in %s", errCrewNetworkPool, crewNetworkPrefixBits, pool)
}

func (p *Provider) crewNetworkPool() (netip.Prefix, error) {
	raw := p.cfg.CrewNetworkPool
	if raw == "" {
		raw = defaultCrewNetworkPool
	}
	pool, err := netip.ParsePrefix(raw)
	if err != nil || !pool.Addr().Is4() || pool.Bits() > crewNetworkPrefixBits || pool.Bits() < crewNetworkPoolMinBits {
		return netip.Prefix{}, fmt.Errorf("%w: %q must be an IPv4 prefix between /%d and /%d", errCrewNetworkPool, raw, crewNetworkPoolMinBits, crewNetworkPrefixBits)
	}
	return pool.Masked(), nil
}

// candidateSubnets lists every /bits subnet of pool in address order.
func candidateSubnets(pool netip.Prefix, bits int) []netip.Prefix {
	if !pool.Addr().Is4() || pool.Bits() > bits {
		return nil
	}
	base := binary.BigEndian.Uint32(pool.Masked().Addr().AsSlice())
	count := uint32(1) << uint(bits-pool.Bits())
	step := uint32(1) << uint(32-bits)
	out := make([]netip.Prefix, 0, count)
	for i := uint32(0); i < count; i++ {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], base+i*step)
		out = append(out, netip.PrefixFrom(netip.AddrFrom4(b), bits))
	}
	return out
}

// foreignRouteInPool reports a host route inside the pool that is not one of
// Docker's own bridges. A crew network's bridge adds a route for its subnet;
// that route is ours and the allocator already avoids the subnet through
// used, so only a route nobody on the daemon owns (a LAN, a VPN) makes the
// pool unusable. The default route (/0) is not a conflict.
func foreignRouteInPool(pool netip.Prefix, routes, docker []netip.Prefix) (netip.Prefix, bool) {
	for _, r := range routes {
		if r.Bits() == 0 || !r.Overlaps(pool) {
			continue
		}
		owned := false
		for _, d := range docker {
			if d == r {
				owned = true
				break
			}
		}
		if !owned {
			return r, true
		}
	}
	return netip.Prefix{}, false
}

func overlapsAny(p netip.Prefix, set []netip.Prefix) bool {
	for _, q := range set {
		if p.Overlaps(q) {
			return true
		}
	}
	return false
}

// hostIPv4Routes reads the IPv4 routing table of the namespace crewshipd runs
// in. A missing table (non-linux) is an empty set.
var hostIPv4Routes = func() ([]netip.Prefix, error) {
	f, err := os.Open("/proc/net/route")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseProcNetRoute(bufio.NewScanner(f))
}

// parseProcNetRoute parses /proc/net/route: destination and mask are
// little-endian hex.
func parseProcNetRoute(sc *bufio.Scanner) ([]netip.Prefix, error) {
	var out []netip.Prefix
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		dst, err1 := hex.DecodeString(f[1])
		mask, err2 := hex.DecodeString(f[7])
		if err1 != nil || err2 != nil || len(dst) != 4 || len(mask) != 4 {
			continue
		}
		addr := netip.AddrFrom4([4]byte{dst[3], dst[2], dst[1], dst[0]})
		ones := 0
		for _, b := range mask {
			for i := 0; i < 8; i++ {
				if b&(1<<uint(i)) != 0 {
					ones++
				}
			}
		}
		out = append(out, netip.PrefixFrom(addr, ones).Masked())
	}
	return out, sc.Err()
}

// serviceNetworkFor is the network a crew's services must be on: the one its
// runtime container actually uses when that container exists, otherwise the
// one crewNetworkFor names. It makes sure that network exists.
func (p *Provider) serviceNetworkFor(ctx context.Context, id, slug string) (string, error) {
	cid, _, err := p.FindCrewContainer(ctx, id, slug)
	if err != nil {
		return "", err
	}
	if cid != "" {
		insp, err := p.client.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
		if err != nil {
			return "", fmt.Errorf("inspect crew runtime network: %w", err)
		}
		if hc := insp.Container.HostConfig; hc != nil && hc.NetworkMode != "" && hc.NetworkMode.IsUserDefined() {
			name := string(hc.NetworkMode)
			if name == p.cfg.Network {
				return name, p.ensureNetwork(ctx, name)
			}
			if name == p.crewNetworkName(id) {
				return p.ensureCrewNetworkNamed(ctx, id, slug)
			}
		}
	}
	return p.ensureCrewNetwork(ctx, id, slug)
}

// moveServiceNetwork attaches a service container to netName under its
// service alias and detaches it from every other network, without recreating
// it. Connect first, so the service is never on no network.
func (p *Provider) moveServiceNetwork(ctx context.Context, containerID string, settings *container.NetworkSettingsSummary, netName, alias string) error {
	attached := map[string]bool{}
	if settings != nil {
		for n := range settings.Networks {
			attached[n] = true
		}
	}
	if attached[netName] && len(attached) == 1 {
		return nil
	}
	if !attached[netName] {
		if _, err := p.client.NetworkConnect(ctx, netName, client.NetworkConnectOptions{
			Container:      containerID,
			EndpointConfig: &network.EndpointSettings{Aliases: []string{alias}},
		}); err != nil {
			return fmt.Errorf("move service %q to network %s: %w", alias, netName, err)
		}
	}
	for n := range attached {
		if n == netName {
			continue
		}
		if _, err := p.client.NetworkDisconnect(ctx, n, client.NetworkDisconnectOptions{Container: containerID}); err != nil {
			return fmt.Errorf("detach service %q from network %s: %w", alias, n, err)
		}
	}
	p.logger.Info("service moved to the crew's network", "service", alias, "network", netName)
	return nil
}

// hostRoutesVisible reports whether this process's routing table is the
// Docker host's: a local daemon socket and not running inside a container.
func (p *Provider) hostRoutesVisible() bool {
	host := p.detected.Host
	if host != "" && !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
		return false
	}
	return !runningInContainer()
}

// runningInContainer is a variable so tests can pin it.
var runningInContainer = func() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}
