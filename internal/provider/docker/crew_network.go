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
	"sort"
	"strings"
	"sync"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
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

// errCrewNetworkUnsupported: this deployment cannot run crew networks yet.
var errCrewNetworkUnsupported = errors.New("crew networks unsupported here")

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
	return provider.CrewNetworkName(p.cfg.Network, id)
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
	if err := p.crewNetworkTopologySupported(); err != nil {
		return "", err
	}
	name := p.crewNetworkName(id)
	crewNetworkAlloc.Lock()
	defer crewNetworkAlloc.Unlock()

	nets, err := p.client.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return "", fmt.Errorf("crew network: list networks: %w", err)
	}
	// A crew network is never less isolated than the instance network it
	// replaces: when that one is internal, the crew network is internal too.
	internal, err := instanceNetworkInternal(nets.Items, p.cfg.Network)
	if err != nil {
		return "", err
	}
	var used []netip.Prefix
	for _, n := range nets.Items {
		if n.Name == name {
			if n.Labels[crewKindLabel] != crewKindLabelValueNet || n.Labels[crewCrewIDLabel] != id ||
				n.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID {
				return "", fmt.Errorf("crew network: %q exists but is not this installation's network for crew %s", name, id)
			}
			if internal && !n.Internal {
				return "", fmt.Errorf("crew network: %q is not internal while the instance network is; remove it so it can be recreated", name)
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
	routes, err := hostIPv4Routes()
	if err != nil {
		return "", fmt.Errorf("crew network: read host routes: %w", err)
	}
	if p.hostRoutesVisible(routes, used) {
		if r, bad := foreignRouteInPool(pool, routes, used); bad {
			return "", fmt.Errorf("%w: %s overlaps host route %s; set container.crew_network_pool", errCrewNetworkPool, pool, r)
		}
	} else if p.cfg.CrewNetworkPool == "" {
		return "", fmt.Errorf("%w: crewship cannot prove it sees the Docker host's routes (a container, a remote daemon, Docker Desktop's VM); set container.crew_network_pool to a range you know is free on that host", errCrewNetworkPool)
	}
	enableIPv6 := false
	for _, subnet := range candidateSubnets(pool, crewNetworkPrefixBits) {
		if overlapsAny(subnet, used) {
			continue
		}
		gw := subnet.Addr().Next()
		_, err := p.client.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver:     "bridge",
			Internal:   internal,
			EnableIPv6: &enableIPv6,
			IPAM: &network.IPAM{
				Driver: "default",
				// Dynamic addresses come only from the upper half, so the
				// fixed service addresses below (serviceAddr) can never be
				// handed to another container.
				Config: []network.IPAMConfig{{Subnet: subnet, Gateway: gw, IPRange: dynamicRange(subnet)}},
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
// it (recreating orphans image-declared anonymous volumes). Connect first so
// the service is never on no network. If a detach fails the connect is rolled
// back, and the result is verified, so EnsureCrewServices either returns with
// the service on exactly netName or returns an error and the crew's work
// does not start on an unconfirmed topology. Open client connections to the
// service (a database session) do not survive the move; clients reconnect.
func (p *Provider) moveServiceNetwork(ctx context.Context, containerID string, settings *container.NetworkSettingsSummary, netName, alias string, fixed netip.Addr) error {
	attached := map[string]bool{}
	if settings != nil {
		for n, ep := range settings.Networks {
			attached[n] = true
			// On the target network but not at its fixed address: detach
			// and attach again at the right one (same container, data kept).
			if n == netName && fixed.IsValid() && ep != nil && ep.IPAddress != fixed {
				if err := p.networkDisconnect(ctx, n, containerID); err != nil {
					return fmt.Errorf("re-address service %q: %w", alias, err)
				}
				delete(attached, n)
			}
		}
	}
	if attached[netName] && len(attached) == 1 {
		return nil
	}
	connected := false
	if !attached[netName] {
		if _, err := p.client.NetworkConnect(ctx, netName, client.NetworkConnectOptions{
			Container:      containerID,
			EndpointConfig: endpointFor(alias, fixed),
		}); err != nil {
			return fmt.Errorf("move service %q to network %s: %w", alias, netName, err)
		}
		connected = true
	}
	var detached []string
	for n := range attached {
		if n == netName {
			continue
		}
		if err := p.networkDisconnect(ctx, n, containerID); err != nil {
			p.rollbackServiceMove(context.WithoutCancel(ctx), containerID, settings, alias, netName, connected, detached)
			return fmt.Errorf("detach service %q from network %s: %w", alias, n, err)
		}
		detached = append(detached, n)
	}
	insp, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("verify service %q network: %w", alias, err)
	}
	if ns := insp.Container.NetworkSettings; ns == nil || len(ns.Networks) != 1 || ns.Networks[netName] == nil ||
		(fixed.IsValid() && ns.Networks[netName].IPAddress != fixed) {
		return fmt.Errorf("service %q is not on exactly network %s at its address after the move", alias, netName)
	}
	p.logger.Info("service moved to the crew's network", "service", alias, "network", netName)
	return nil
}

// rollbackServiceMove puts a service back where it was after a move failed
// part-way: on every old network it was already detached from (with its old
// aliases and address), and off the new one if this move attached it. What
// cannot be restored is logged; the move's error is returned either way.
func (p *Provider) rollbackServiceMove(ctx context.Context, containerID string, settings *container.NetworkSettingsSummary,
	alias, netName string, connected bool, detached []string) {
	for _, n := range detached {
		ep := &network.EndpointSettings{Aliases: []string{alias}}
		if settings != nil && settings.Networks[n] != nil {
			old := settings.Networks[n]
			ep = &network.EndpointSettings{Aliases: old.Aliases, IPAMConfig: old.IPAMConfig}
		}
		if _, err := p.client.NetworkConnect(ctx, n, client.NetworkConnectOptions{Container: containerID, EndpointConfig: ep}); err != nil {
			p.logger.Error("service network move rolled back incompletely; not re-attached to its old network",
				"service", alias, "old", n, "new", netName, "error", err)
		}
	}
	if connected {
		if err := p.networkDisconnect(ctx, netName, containerID); err != nil {
			p.logger.Error("service network move rolled back incompletely; still on the new network",
				"service", alias, "new", netName, "error", err)
		}
	}
}

// networkDisconnect is the one disconnect call; tests replace it through
// networkDisconnectHook to inject a failure.
func (p *Provider) networkDisconnect(ctx context.Context, netName, containerID string) error {
	if p.networkDisconnectHook != nil {
		if err := p.networkDisconnectHook(netName); err != nil {
			return err
		}
	}
	_, err := p.client.NetworkDisconnect(ctx, netName, client.NetworkDisconnectOptions{Container: containerID})
	return err
}

// hostRoutesVisible reports whether this process provably sees the Docker
// host's routing table. A local socket alone does not prove it (Docker
// Desktop runs the daemon in a VM; a containerised crewship has its own
// namespace), so the proof is that the daemon's own bridge subnets appear as
// routes here: a process sharing the daemon host's network namespace sees
// a route for every bridge the daemon created. Without that proof, an
// explicit pool is required.
func (p *Provider) hostRoutesVisible(routes, dockerSubnets []netip.Prefix) bool {
	host := p.detected.Host
	if host != "" && !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
		return false
	}
	if runningInContainer() {
		return false
	}
	for _, d := range dockerSubnets {
		for _, r := range routes {
			if r == d {
				return true
			}
		}
	}
	return false
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

// crewNetworkTopologySupported refuses crew networks unless crewshipd runs on
// the Docker host itself (first stage). A containerised crewshipd (the
// production compose file: its own container on the internal instance
// network, the daemon behind a socket proxy) is not attached to crew
// networks, so a listed crew could not reach it; a remote daemon puts the
// crews on another machine. Refusing fails the listed crew's start with a
// reason instead of starting crews that cannot work.
func (p *Provider) crewNetworkTopologySupported() error {
	host := p.detected.Host
	if host != "" && !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
		return fmt.Errorf("%w: the Docker daemon is not local (%s); crew networks need crewshipd on the Docker host", errCrewNetworkUnsupported, host)
	}
	if runningInContainer() {
		return fmt.Errorf("%w: crewshipd runs in a container and is not attached to crew networks; crew networks need crewshipd on the Docker host", errCrewNetworkUnsupported)
	}
	return nil
}

// instanceNetworkInternal reports whether the instance network is internal.
// A missing instance network is an error: there is nothing to copy the
// isolation from, and "not internal" is not a safe guess.
func instanceNetworkInternal(nets []network.Summary, name string) (bool, error) {
	if name == "" {
		// No instance network: crews run on Docker's default bridge,
		// which is never internal.
		return false, nil
	}
	for _, n := range nets {
		if n.Name == name {
			return n.Internal, nil
		}
	}
	return false, fmt.Errorf("crew network: instance network %q not found; it must exist before a crew network is created", name)
}

// Fixed service addresses on a crew's own /27 (#1368 + #2240): services take
// .4 .. .15 by the sorted order of their names, dynamic containers (the
// runtime, anything else) .16 .. .31. A fixed address is what lets the fence
// open an exact service endpoint and the runtime resolve it through
// ExtraHosts without DNS, and it never moves to another container.
const (
	serviceAddrFirst = 4
	serviceAddrLast  = 15
)

// dynamicRange is the upper half of a crew subnet.
func dynamicRange(subnet netip.Prefix) netip.Prefix {
	base := subnet.Masked().Addr().As4()
	base[3] += 16
	return netip.PrefixFrom(netip.AddrFrom4(base), subnet.Bits()+1)
}

// serviceAddrs assigns each declared service its fixed address on the crew
// subnet. Deterministic: the same names always get the same addresses.
func serviceAddrs(subnet netip.Prefix, names []string) (map[string]netip.Addr, error) {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if len(sorted) > serviceAddrLast-serviceAddrFirst+1 {
		return nil, fmt.Errorf("a crew on its own network can declare at most %d services", serviceAddrLast-serviceAddrFirst+1)
	}
	base := subnet.Masked().Addr().As4()
	out := make(map[string]netip.Addr, len(sorted))
	for i, n := range sorted {
		a := base
		a[3] += byte(serviceAddrFirst + i)
		out[n] = netip.AddrFrom4(a)
	}
	return out, nil
}

// crewSubnet returns the subnet of crew id's own network, or false when the
// crew is not on one (or the network does not exist yet).
func (p *Provider) crewSubnet(ctx context.Context, id, slug string) (netip.Prefix, bool, error) {
	if !p.crewNetworkWanted(id, slug) {
		return netip.Prefix{}, false, nil
	}
	// NetworkList rather than NetworkInspect: it is already on the socket
	// proxy allowlist and carries the IPAM config.
	name := p.crewNetworkName(id)
	nets, err := p.client.NetworkList(ctx, client.NetworkListOptions{Filters: make(client.Filters).Add("name", name)})
	if err != nil {
		return netip.Prefix{}, false, err
	}
	var cfgs []network.IPAMConfig
	found := false
	for _, n := range nets.Items {
		if n.Name == name {
			cfgs, found = n.IPAM.Config, true
		}
	}
	if !found {
		return netip.Prefix{}, false, fmt.Errorf("crew network %s does not exist", name)
	}
	for _, c := range cfgs {
		if c.Subnet.IsValid() && c.Subnet.Addr().Is4() {
			if !c.IPRange.IsValid() {
				return netip.Prefix{}, false, fmt.Errorf("crew network %s has no dynamic range; remove it so it can be recreated with fixed service addresses", p.crewNetworkName(id))
			}
			return c.Subnet, true, nil
		}
	}
	return netip.Prefix{}, false, fmt.Errorf("crew network %s has no IPv4 subnet", p.crewNetworkName(id))
}

func serviceNames(team provider.CrewConfig) []string {
	out := make([]string, 0, len(team.Services))
	for _, s := range team.Services {
		out = append(out, s.Name)
	}
	return out
}

// endpointFor is a service's endpoint on a network: its alias, and its fixed
// address when it has one.
func endpointFor(alias string, fixed netip.Addr) *network.EndpointSettings {
	ep := &network.EndpointSettings{Aliases: []string{alias}}
	if fixed.IsValid() {
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: fixed}
	}
	return ep
}

// serviceExtraHosts maps each declared service name to its fixed address on
// the crew's own network, for the runtime container's /etc/hosts. A fenced
// agent cannot query DNS; it resolves its services from here. Empty for a
// crew without its own network or without services.
func (p *Provider) serviceExtraHosts(ctx context.Context, team provider.CrewConfig) ([]string, error) {
	if len(team.Services) == 0 {
		return nil, nil
	}
	subnet, ok, err := p.crewSubnet(ctx, team.ID, team.Slug)
	if err != nil || !ok {
		return nil, err
	}
	addrs, err := serviceAddrs(subnet, serviceNames(team))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, n := range serviceNames(team) {
		out = append(out, n+":"+addrs[n].String())
	}
	sort.Strings(out)
	return out, nil
}

// sameServiceHosts compares the service entries of a container's ExtraHosts
// (everything but host.docker.internal) with the wanted set.
func sameServiceHosts(have, want []string) bool {
	var h []string
	for _, e := range have {
		if !strings.HasPrefix(e, "host.docker.internal:") {
			h = append(h, e)
		}
	}
	sort.Strings(h)
	w := append([]string(nil), want...)
	sort.Strings(w)
	if len(h) != len(w) {
		return false
	}
	for i := range h {
		if h[i] != w[i] {
			return false
		}
	}
	return true
}
