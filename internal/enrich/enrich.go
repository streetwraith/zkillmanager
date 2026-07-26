// Package enrich derives the stable, SDE-backed facts stored on each killmail.
package enrich

// SpaceType is the space classification stored per kill -- lscan's main
// aggregation axis.
type SpaceType int8

const (
	Highsec  SpaceType = 0
	Lowsec   SpaceType = 1
	Nullsec  SpaceType = 2
	Wormhole SpaceType = 3
	Pochven  SpaceType = 4
)

// ClassifySpace maps a system's region wormhole-class (sde.map_regions.wormhole_class_id,
// uniform across a wormhole/Pochven region; pass 0 for a NULL class) plus its
// security_status to a SpaceType.
//
// SDE wormhole-class enum: 1-6 = C1-C6, 7/8/9 = hi/lo/null k-space, 12 = Thera,
// 13 = C13 shattered, 14-18 = drifter wormholes, 19-23 = abyssal/void, 25 = Pochven.
//
// Only abyssal/void (19-23) is unbucketed (ok = false) -- it's instanced and can't host
// normal PvP, so such a kill is logged and skipped. Everything else falls through to a
// per-system security split: k-space classes 7/8/9 are region-wide and coarse (Rancer is
// lowsec inside a class-7 region), and several live NPC/edge regions carry no class at all
// (Syndicate, the Jove regions, Zarzakh, Exordium) -- security_status is the authority for
// all of them.
func ClassifySpace(regionWormholeClass int, security float64) (SpaceType, bool) {
	switch regionWormholeClass {
	case 25:
		return Pochven, true
	case 1, 2, 3, 4, 5, 6, 12, 13, 14, 15, 16, 17, 18:
		return Wormhole, true
	case 19, 20, 21, 22, 23:
		return 0, false
	default:
		switch {
		case security >= 0.45:
			return Highsec, true
		case security > 0.0:
			return Lowsec, true
		default:
			return Nullsec, true
		}
	}
}

// SystemRecord is one row of the SDE system->space lookup (the resolver's input),
// loaded from sde.map_solar_systems joined to sde.map_regions.
type SystemRecord struct {
	SystemID            int
	ConstellationID     int
	RegionID            int
	RegionWormholeClass int // 0 for a NULL region class
	Security            float64
}

// System is the resolved, stored-shape info for one solar system.
type System struct {
	ConstellationID int
	RegionID        int
	SpaceType       SpaceType
}

// Resolver is an in-memory system->System map. EVE's system data is static (changes
// only on an SDE import), so we load it once at startup and every ingest is a map hit.
// Systems that classify as abyssal/void are omitted, so a miss means "don't store".
type Resolver struct {
	systems map[int]System
}

func NewResolver(records []SystemRecord) *Resolver {
	m := make(map[int]System, len(records))
	for _, r := range records {
		st, ok := ClassifySpace(r.RegionWormholeClass, r.Security)
		if !ok {
			continue
		}
		m[r.SystemID] = System{ConstellationID: r.ConstellationID, RegionID: r.RegionID, SpaceType: st}
	}
	return &Resolver{systems: m}
}

func (r *Resolver) Resolve(systemID int) (System, bool) {
	s, ok := r.systems[systemID]
	return s, ok
}

// Len is the number of classified (storable) systems.
func (r *Resolver) Len() int { return len(r.systems) }
