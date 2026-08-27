package enrich

// Stealth ship classification: a hull is stealth if it is hard to see, because it takes a
// covert ops cloak or because it stays off directional scan (SDE attribute 1958,
// dscanImmune). Source of truth: /stealth_ships.txt -- keep these lists in sync with that
// seed. StealthGroupIDs are SDE group ids whose every member counts as stealth (expanded to
// type ids via the SDE, so new hulls in a group are picked up automatically);
// StealthTypeIDs are individual stealth hulls that sit in non-stealth groups. The collector
// materializes the expanded set into killmails.stealth_ship at startup; readers join it
// (see PROJECT.md).
var (
	StealthGroupIDs = []int{
		963,  // Strategic Cruiser
		830,  // Covert Ops
		833,  // Force Recon Ship
		906,  // Combat Recon Ship -- no covert cloak, but dscanImmune (SDE attribute 1958)
		834,  // Stealth Bomber
		1202, // Blockade Runner
		5087, // Special Edition Yachts
	}
	StealthTypeIDs = []int{
		32790, // Etana
		33468, // Astero
		33470, // Stratios
		33553, // Stratios Emergency Responder
		34590, // Victorieux Luxury Yacht
		33697, // Prospect
		42245, // Rabisu
		77114, // Metamorphosis
		85087, // Tholos
		85086, // Cenotaph
		89607, // Odysseus
	}
)
