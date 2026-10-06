package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/jonradoff/lofp/internal/gameworld"
)

// Combat stance constants
const (
	StanceNormal    = 0
	StanceOffensive = 1
	StanceDefensive = 2
	StanceBerserk   = 3
	StanceWary      = 4
)

var stanceNames = map[int]string{
	StanceNormal:    "normal",
	StanceOffensive: "offensive",
	StanceDefensive: "defensive",
	StanceBerserk:   "berserk",
	StanceWary:      "wary",
}

// CombatTarget tracks who a player/monster is fighting.
type CombatTarget struct {
	IsMonster  bool
	MonsterID  int
	PlayerName string
}

// ---- XP per build point table (from GM Manual) ----
// Index = level, value = XP needed per ONE build point at that level.
// Total build points at a level = 20 + (10 * level).
// Build points are earned incrementally as XP accumulates.
// Level increases when total build points reach 20 + 10*(level+1).

var xpPerBP = []int{
	0,                                                     // level 0 (unused)
	100, 200, 400, 600, 800, 1000, 1200, 1400, 1600, 2000, // 1-10
	2400, 2700, 3200, 4000, 4800, 5600, 6400, 7200, 8000, 8800, // 11-20
	9600, 10400, 11200, 12000, 12800, 13600, 14400, 15200, 16000, 16800, // 21-30
	17600, 18400, 19200, 20000, 20800, 21600, 22400, 23200, 24000, 24800, // 31-40
	25600, 26400, 27200, 28000, 28800, 29600, 30400, 31200, 32000, 32800, // 41-50
	33600, 34400, 35200, 36000, 36800, 37600, 38400, 39200, 40000, 40800, // 51-60
	51600, 53200, 54800, 56400, 58000, 59600, 61200, 62800, 64400, 66000, // 61-70
	67600, 69200, 70800, 72400, 74000, 75600, 77200, 78800, 80400, 82000, // 71-80
	83600, 85200, 86800, 88400, 90000, 91600, 93200, 94800, 96400, 98000, // 81-90
	99600, 101200, 102800, 104400, 106000, 107600, 109200, 110800, 112400, 114000, // 91-100
}

// getXPPerBP returns the XP cost per build point at a given level.
func getXPPerBP(level int) int {
	if level <= 0 {
		return 100
	}
	if level < len(xpPerBP) {
		return xpPerBP[level]
	}
	// Formula for level 136+: 170000 + (level-135)*1600
	return 170000 + (level-135)*1600
}

// buildPointsForLevel returns total build points at a given level.
func buildPointsForLevel(level int) int {
	return 20 + 10*level
}

// recalcBuildPoints recalculates a player's build points and level from their XP,
// and applies the HP/Fatigue/Mana/Psi level-up bonus for every level gained.
//
// The bonus is applied here, centrally, rather than left to each caller: many XP
// sources (crafting, lockpicking, healing/first-aid — see the `.Experience +=`
// sites across crafting.go/skills.go) add straight to player.Experience with no
// leveling logic at all, relying on some *later* call to recalcBuildPoints to
// catch player.Level up. But STATUS and EXP/EXPERIENCE also call recalcBuildPoints
// (to keep the displayed BP current) and previously ignored its returned bool —
// so if a player checked STATUS/EXP before their next real combat kill, Level
// would silently jump to match accumulated XP with no bonus applied at all, and
// since oldLevel is read fresh from player.Level on every call, that missed gain
// could never be recovered: the next real recalc would see Level already caught
// up and report leveledUp == false forever. Applying the bonus inside this single
// function, proportional to the actual number of levels gained (not a flat
// single-level increment gated by a bool), means no caller can advance Level
// without it — and a multi-level jump in one XP grant (e.g. a large @exp) now
// grants every level's worth of bonus, not just one.
//
// Build points are earned incrementally: each BP costs XP/BP at the player's current level.
func recalcBuildPoints(player *Player) (leveledUp bool) {
	// Calculate total BP earned from total XP
	xpRemaining := player.Experience
	bp := 30 // starting build points (matches CreateNewPlayer)
	lvl := 1

	for {
		rate := getXPPerBP(lvl)
		targetBP := buildPointsForLevel(lvl + 1) // BP needed for next level
		bpToNextLevel := targetBP - bp
		xpForNextLevel := bpToNextLevel * rate

		if xpRemaining >= xpForNextLevel {
			xpRemaining -= xpForNextLevel
			bp = targetBP
			lvl++
		} else {
			// Partial progress within current level
			if rate > 0 {
				bp += xpRemaining / rate
			}
			break
		}

		if lvl > 200 { // safety cap
			break
		}
	}

	oldLevel := player.Level
	// BuildPoints = total earned BP minus BP spent on skills
	spent := playerBPSpent(player)
	player.BuildPoints = bp - spent
	if player.BuildPoints < 0 {
		player.BuildPoints = 0
	}
	player.Level = lvl

	levelsGained := player.Level - oldLevel
	for i := 0; i < levelsGained; i++ {
		player.MaxBodyPoints += player.Constitution / 10
		player.MaxFatigue += player.Constitution / 15
		player.MaxMana += player.Empathy / 15
		player.MaxPsi += player.Willpower / 10
	}
	if levelsGained > 0 {
		player.BodyPoints = player.MaxBodyPoints
		player.Fatigue = player.MaxFatigue
		player.Mana = player.MaxMana
		player.Psi = player.MaxPsi
	}

	return levelsGained > 0
}

// xpForNextBP returns the XP cost for the player's next build point.
func xpForNextBP(player *Player) int {
	return getXPPerBP(player.Level)
}

// xpProgressInLevel returns (xp earned in current level, xp needed for next level).
func xpProgressInLevel(player *Player) (earned int, needed int) {
	// Sum XP consumed by all levels before current
	xpConsumed := 0
	for lvl := 1; lvl < player.Level; lvl++ {
		bpInLevel := 10 // 10 BP per level
		xpConsumed += bpInLevel * getXPPerBP(lvl)
	}
	earned = player.Experience - xpConsumed
	if earned < 0 {
		earned = 0
	}
	needed = 10 * getXPPerBP(player.Level)
	return
}

// ---- Weather combat modifiers (from GM Manual) ----

var weatherAttackMod = map[int]int{
	4: -10, 5: -20, 6: -30, 7: -40, 8: -50, // rain→hurricane
	10: -10, 11: -20, 12: -30, 13: -40, 14: -50, // sleet→blizzard
}

func (e *GameEngine) weatherMod(roomNum int) int {
	room := e.rooms[roomNum]
	if room == nil || !isOutdoorTerrain(room.Terrain) {
		return 0
	}
	region := room.Region
	wea, ok := e.RegionWeather[region]
	if !ok {
		return 0
	}
	if mod, ok := weatherAttackMod[wea]; ok {
		return mod
	}
	return 0
}

// joinWithAnd joins a list of strings with commas and "and" before the last element.
func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

// ---- Damage severity tiers (single-word descriptors for combat hit messages;
// vocabulary sourced from original session captures — see original/chandra_wastes.txt,
// e.g. "Minor burn to right leg. [17 Damage]", "Ghastly burn to head. [39 Damage]") ----

var severityWords = [12]string{
	"Puny", "Feeble", "Grazing", "Insignificant", "Fine", "Minor",
	"Passable", "Good", "Serious", "Grisly", "Severe", "Ghastly",
}

// damageSeverity returns a single-word severity descriptor for a hit, e.g. "Minor"
// or "Ghastly", for use in "<Severity> <type> to <part>. [N Damage]" combat lines.
// Severity is based on the damage as a fraction of the target's max HP (via the
// same 1-12 wound-level bands used for persistent wound tracking), not the raw
// damage number alone — the same absolute damage is a much bigger deal against a
// weak monster than a tough one, which is why the same raw damage shows up under
// different severity words for different targets in the original session captures.
func damageSeverity(dmg, maxHP int) string {
	level := woundLevelFromDamage(dmg, maxHP)
	if level < 1 {
		level = 1
	}
	if level > len(severityWords) {
		level = len(severityWords)
	}
	return severityWords[level-1]
}

// simplifiedDamageTier returns a simplified damage description for third-person room broadcasts.
func simplifiedDamageTier(dmg int) string {
	switch {
	case dmg <= 5:
		return "Minor damage"
	case dmg <= 12:
		return "Good damage"
	case dmg <= 20:
		return "Major damage"
	case dmg <= 30:
		return "Massive damage"
	case dmg <= 45:
		return "Devastating damage"
	default:
		return "Awesome damage"
	}
}

// strengthenSteelBonus is the flat bonus Strengthen Steel (psi discipline 7)
// grants a wielded steel weapon while maintained — see GM Pages/psionics.html
// #8: "a more sturdy [hardness] and sharper [to-hit] implement... a +15 bonus
// for the weapon. It has no effect on armor." The doc gives one +15 figure
// covering both "sturdy" and "sharper"; applying it to both readings (attack
// rating in playerAttackRating, and break-resistance here) is a judgment call
// in the absence of the original engine's own split, if any.
const strengthenSteelBonus = 15

// isSteelWeapon reports whether item carries the "steel" adjective in any of
// its three adjective slots — the qualifying condition for both Strengthen
// Steel (discipline 7) and Transform Steel (discipline 16), which only affect
// steel weapons specifically, not just any weapon.
func (e *GameEngine) isSteelWeapon(item *InventoryItem) bool {
	if item == nil {
		return false
	}
	steelAdj := e.adjByName("steel")
	if steelAdj == 0 {
		return false
	}
	return item.Adj1 == steelAdj || item.Adj2 == steelAdj || item.Adj3 == steelAdj
}

// weaponHardness computes a weapon's break-resistance ("Strength" in the
// Weapon Clash message): a base value from weight/damage plus the BREAKMOD
// modifier (from adjnoun.scr) for each of the weapon instance's adjectives
// (Adj1/Adj2/Adj3), plus the instance's own GM-editable HardnessMod (@editem),
// plus strengthenSteelBonus while the wielding player maintains Strengthen
// Steel (psi 7) on a steel weapon. player may be nil for a non-gameplay
// lookup (e.g. @iexamine's debug display), which just omits that last part.
func (e *GameEngine) weaponHardness(player *Player, wielded *InventoryItem, weaponDef *gameworld.ItemDef) int {
	if weaponDef == nil {
		return 0
	}
	if wielded == nil {
		return weaponDef.Weight*3 + weaponDef.Parameter1*2
	}
	hardness := e.weaponHardnessFor(weaponDef, wielded.Adj1, wielded.Adj2, wielded.Adj3, wielded.Val2, wielded.HardnessMod)
	if player != nil && player.ActivePsi[7] && e.isSteelWeapon(wielded) {
		hardness += strengthenSteelBonus
	}
	return hardness
}

// weaponHardnessFor is the adjective/instance-agnostic core of weaponHardness,
// usable for both a wielded InventoryItem and a RoomItem/InventoryItem lying
// around that's never been wielded (e.g. for a Weaponsmithing EXAMINE appraisal).
// val2 is the instance's magic bonus, used only by missileClashBonus.
func (e *GameEngine) weaponHardnessFor(weaponDef *gameworld.ItemDef, adj1, adj2, adj3, val2, hardnessMod int) int {
	if weaponDef == nil {
		return 0
	}
	hardness := weaponDef.Weight*3 + weaponDef.Parameter1*2
	for _, adjID := range []int{adj1, adj2, adj3} {
		if adjID == 0 {
			continue
		}
		hardness += e.breakMods[adjID]
	}
	hardness += hardnessMod
	hardness += missileClashBonus(weaponDef, adj1, adj2, adj3, val2)
	return hardness
}

// Missile weapon clash resistance. Bows/crossbows are light (Weight 1-2 and a
// small PARAMETER1 for most bows), so the weight/damage base formula gave them a
// hardness of ~20-35 against a 2d100 clash roll — nearly every clash damaged
// them, and players reported bows damaged before even reaching a hunting spot.
// Not original data (a design change per the game owner's request, 2026-09-23).
const (
	missileClashBase      = 50 // every missile weapon
	missilePremiumWoodMod = 25 // made from mirmach, oak, rosewood or uquart
)

// missilePremiumWoodAdjs are the ADJNOUN.SCR adjective IDs of the woods that
// give a missile weapon extra clash resistance — carried onto crafted bows as
// an Adj slot by buildCraftAdjs from the raw wood's PARAMETER1.
var missilePremiumWoodAdjs = map[int]bool{
	196: true, // mirmach
	216: true, // oak
	366: true, // rosewood
	577: true, // uquart
}

// missileClashBonus is the extra clash resistance (on top of weaponHardnessFor's
// base + BREAKMODs) for a missile weapon: missileClashBase, missilePremiumWoodMod
// for a premium wood, and +10/+20/+30 for Enchantment I/II/III. The enchantment
// tier is normally already covered by the enchanted/ensorcelled/eldritch
// adjective's own BREAKMOD (ADJNOUN.SCR: 523=10, 524=20, 525=30), so it's only
// added here when that tag is missing — castEnchantmentSpell skips the tag when
// all three Adj slots are full (typical for a crafted bow), leaving just the
// Val2 bonus (10/20/30) as the record of which spell was cast (same fallback as
// castDisenchantSpell).
func missileClashBonus(weaponDef *gameworld.ItemDef, adj1, adj2, adj3, val2 int) int {
	if weaponDef == nil || !isLauncherWeaponType(weaponDef.Type) {
		return 0
	}
	bonus := missileClashBase
	premium, enchantTagged := false, false
	for _, adjID := range []int{adj1, adj2, adj3, weaponDef.Adjective, weaponDef.Adjective2} {
		if missilePremiumWoodAdjs[adjID] {
			premium = true
		}
		if disenchantAdjIDs[adjID] {
			enchantTagged = true
		}
	}
	if premium {
		bonus += missilePremiumWoodMod
	}
	if !enchantTagged {
		switch {
		case val2 >= 30:
			bonus += 30
		case val2 >= 20:
			bonus += 20
		case val2 >= 10:
			bonus += 10
		}
	}
	return bonus
}

// ---- Weapon damage-type kill flavors (from original session capture) ----

// meleeSlashPunctureKillFlavors describes a killing slash or (melee) puncture blow
// itself, replacing the normal severity/damage line the same way elementalKillFlavors
// does for elemental damage — see weaponKillFlavor.
var meleeSlashPunctureKillFlavors = []string{
	"Shot to back creates peep-hole through heart.",
	"Slash to spine exposes innards.",
	"Brutal abdominal cut results in disembowelment.",
	"Precise strike between shoulders fillets spine.",
}

// rangedPunctureKillFlavors supplements meleeSlashPunctureKillFlavors for puncture
// kills specifically from bow/crossbow (and other thrown/projectile) weapons.
var rangedPunctureKillFlavors = []string{
	"Terrible gouge skewers spleen.",
	"Horrible downward thrust lacerates liver.",
}

var crushKillFlavors = []string{
	"Blow to body ruptures internal organs!",
}

// weaponKillFlavor returns a random description of a killing weapon blow for dtype
// ("slash", "puncture", "crush", as returned by damageTypeForWeapon), or "" if none
// exists — callers should fall back to the normal severity/damage line in that case.
// ranged should be true for bow/crossbow/thrown weapons, widening the puncture pool
// with projectile-specific text.
func weaponKillFlavor(dtype string, ranged bool) string {
	var pool []string
	switch dtype {
	case "slash":
		pool = meleeSlashPunctureKillFlavors
	case "puncture":
		pool = meleeSlashPunctureKillFlavors
		if ranged {
			pool = append(append([]string{}, meleeSlashPunctureKillFlavors...), rangedPunctureKillFlavors...)
		}
	case "crush":
		pool = crushKillFlavors
	}
	if len(pool) == 0 {
		return ""
	}
	return pool[rand.Intn(len(pool))]
}

// isRangedWeaponType reports whether a weapon type fires/throws its damage rather
// than delivering it in melee — used to pick the wider ranged-puncture kill-flavor
// pool for bow/crossbow (and thrown) kills.
func isRangedWeaponType(weaponType string) bool {
	switch weaponType {
	case "BOW_WEAPON", "THROWN_WEAPON", "STABTHROWN", "POLETHROWN", "DRAKIN_THROWN", "HANDGUN", "RIFLE":
		return true
	}
	return false
}

// isLauncherWeaponType reports whether a weapon type launches ammunition (bows,
// crossbows, slings, blowguns — all BOW_WEAPON in ITEMWEAP.SCR — plus the
// Ancient guns). Narrower than isRangedWeaponType: thrown weapons like the spear,
// javelin and harpoon double as everyday melee weapons, so they don't get the
// missile clash bonus (see missileClashBonus).
func isLauncherWeaponType(weaponType string) bool {
	switch weaponType {
	case "BOW_WEAPON", "HANDGUN", "RIFLE":
		return true
	}
	return false
}

// ---- Attack verb by weapon type (from session capture) ----

func attackVerb(weaponDef *gameworld.ItemDef) (selfVerb, thirdVerb, dmgNoun string) {
	if weaponDef == nil {
		return "swing", "swings", "strike"
	}
	switch weaponDef.Type {
	case "SLASH_WEAPON", "TWOHAND_WEAPON", "CLAW_WEAPON", "DRAKIN_SLASH":
		return "swing", "swings", "slash"
	case "PUNCTURE_WEAPON", "POLE_WEAPON", "POLETHROWN", "DRAKIN_POLE", "STABTHROWN":
		return "thrust", "thrusts", "strike"
	case "CRUSH_WEAPON", "BLUNT_WEAPON", "DRAKIN_CRUSH":
		return "swing", "swings", "strike"
	case "BOW_WEAPON", "THROWN_WEAPON", "DRAKIN_THROWN":
		return "fire", "fires", "strike"
	case "BITE_WEAPON":
		return "bite", "bites", "bite"
	default:
		return "swing", "swings", "strike"
	}
}

func monsterAttackVerb(def *gameworld.MonsterDef, items map[int]*gameworld.ItemDef) (verb, dmgNoun string) {
	if len(def.Weapons) > 0 {
		wep := items[def.Weapons[0].Archetype]
		if wep != nil {
			_, v, dn := attackVerb(wep)
			return v + " at", dn
		}
	}
	if def.BodyType == "ANIMAL" || def.BodyType == "AVINE" {
		return "slashes at", "slash"
	}
	return "swings at", "strike"
}

// ---- Weapon skill mapping ----

// isWeaponItemType reports whether an item type is a melee/ranged weapon
// (as opposed to armor, shields, or non-combat items).
func isWeaponItemType(itemType string) bool {
	switch itemType {
	case "SLASH_WEAPON", "TWOHAND_WEAPON", "CLAW_WEAPON", "DRAKIN_SLASH",
		"PUNCTURE_WEAPON", "POLE_WEAPON", "POLETHROWN", "DRAKIN_POLE", "STABTHROWN",
		"CRUSH_WEAPON", "BLUNT_WEAPON", "DRAKIN_CRUSH",
		"BOW_WEAPON", "THROWN_WEAPON", "DRAKIN_THROWN", "BITE_WEAPON":
		return true
	default:
		return false
	}
}

func weaponSkillForType(itemType string) int {
	switch itemType {
	case "SLASH_WEAPON", "TWOHAND_WEAPON":
		return 13
	case "CRUSH_WEAPON", "BLUNT_WEAPON":
		return 9
	case "PUNCTURE_WEAPON", "STABTHROWN":
		return 13
	case "POLE_WEAPON", "POLETHROWN":
		return 25
	case "BOW_WEAPON":
		return 3
	case "THROWN_WEAPON":
		return 19
	case "DRAKIN_SLASH", "DRAKIN_CRUSH", "DRAKIN_POLE", "DRAKIN_THROWN":
		return 16
	case "CLAW_WEAPON", "BITE_WEAPON":
		return 4
	default:
		return 13
	}
}

// clawGrowthWeaponArch is ITEMWEAP.SCR #279 ("Claws"), CLAW_WEAPON/PARAMETER1=5 —
// the natural weapon granted by the Claw Growth spell (518). Used as a virtual
// weapon def only (never assigned to player.Wielded), so claws automatically get
// normal weapon-type damage/skill math (see playerDamage, playerAttackRating,
// weaponSkillForType → skill 4 Natural Weapons) while remaining exempt from
// wielded-weapon-only effects like weapon clash damage/breaking and Sharpness/Val2
// enchantment bonuses, since there's no real InventoryItem instance to mutate.
const clawGrowthWeaponArch = 279

// currentWeaponDef returns the ItemDef for the player's current attack — their
// actual wielded weapon, or (if unarmed) natural claws while Claw Growth is active,
// or nil for ordinary bare-handed/Martial Arts combat.
func (e *GameEngine) currentWeaponDef(player *Player) *gameworld.ItemDef {
	if player.Wielded != nil {
		return e.items[player.Wielded.Archetype]
	}
	if !player.ClawGrowthExpiry.IsZero() && time.Now().Before(player.ClawGrowthExpiry) {
		return e.items[clawGrowthWeaponArch]
	}
	return nil
}

// specializableWeaponSkills are the weapon skills a player may pick a
// specific weapon type to specialize in via SPECIALIZE.
var specializableWeaponSkills = map[int]bool{
	9:  true, // Crushing Weapons
	13: true, // Edged Weapons
	16: true, // Drakin Weapons
	19: true, // Thrown Weapons
	25: true, // Polearms
}

// weaponSpecializationRank returns the player's specialization rank (0-5) for
// the given weapon, keyed by its base noun ID (e.g. the noun for "broadsword").
func (e *GameEngine) weaponSpecializationRank(player *Player, weaponDef *gameworld.ItemDef) int {
	if player.WeaponSpecialization == nil || weaponDef == nil {
		return 0
	}
	return player.WeaponSpecialization[weaponDef.NameID]
}

// ---- To-Hit Calculation ----

func calcToHit(attackRating, defenseRating int) int {
	toHit := 50 + defenseRating - attackRating
	if toHit < 5 {
		toHit = 5
	}
	if toHit > 95 {
		toHit = 95
	}
	return toHit
}

func (e *GameEngine) playerAttackRating(player *Player, weaponDef *gameworld.ItemDef) int {
	rating := 50
	rating += player.Level * 3
	if weaponDef != nil {
		skillID := weaponSkillForType(weaponDef.Type)
		rating += (player.Skills[skillID] + divineBlessingBonus(player)) * 5 // +5 per weapon skill rank (from skills.txt)
	} else {
		// Unarmed: martial arts skill
		rating += (player.Skills[24] + divineBlessingBonus(player)) * 5 // Martial Arts +5 per rank
	}
	if weaponDef != nil && (weaponDef.Type == "BOW_WEAPON" || weaponDef.Type == "THROWN_WEAPON") {
		rating += player.Agility / 5
	} else {
		rating += player.Strength / 5
	}
	switch player.Stance {
	case StanceOffensive:
		rating += 15
	case StanceDefensive:
		rating -= 15
	case StanceBerserk:
		rating += 25
	case StanceWary:
		rating -= 5
	}
	switch player.Position {
	case 1:
		rating -= 20
	case 2:
		rating -= 30
	case 3:
		rating -= 10
	}
	// Sharpness = non-magical to-hit bonus, Val2 = magical enchantment bonus
	if player.Wielded != nil {
		rating += player.Wielded.Sharpness + player.Wielded.Val2
		if player.ActivePsi[7] && e.isSteelWeapon(player.Wielded) {
			rating += strengthenSteelBonus
		}
	}
	return rating
}

// armorEnchantBonus sums Sharpness (quality) + Val2 (magic enchantment) from all worn ARMOR items
// and the equipped shield (if any).
func armorEnchantBonus(player *Player, items map[int]*gameworld.ItemDef) int {
	bonus := 0
	for _, worn := range player.Worn {
		def := items[worn.Archetype]
		if def != nil && def.Type == "ARMOR" {
			bonus += worn.Sharpness + worn.Val2
		}
	}
	if player.OffHand != nil {
		def := items[player.OffHand.Archetype]
		if def != nil && (def.Type == "SHIELD" || containsFlag(def.Flags, "SHIELDGRIP")) {
			bonus += player.OffHand.Sharpness + player.OffHand.Val2
		}
	}
	return bonus
}

// shieldDefenseBonus returns the base defense bonus from a wielded shield's Parameter1,
// or — for a SHIELDGRIP off-hand weapon like the main-gauche (ITEMSNEW.SCR 9110) — the
// greater of 3 or the player's Two Weapons skill level, per the designer's spreadsheet
// ("Whichever is greater: 3 or Two-Weapon style skill level"). SHIELDGRIP items are real
// weapon types (not SHIELD), so this doesn't just read Parameter1 like a normal shield.
func shieldDefenseBonus(player *Player, items map[int]*gameworld.ItemDef) int {
	if player.OffHand == nil {
		return 0
	}
	def := items[player.OffHand.Archetype]
	if def == nil {
		return 0
	}
	if def.Type == "SHIELD" {
		return def.Parameter1
	}
	if containsFlag(def.Flags, "SHIELDGRIP") {
		bonus := player.Skills[1] // Two Weapons
		if bonus < 3 {
			bonus = 3
		}
		return bonus
	}
	return 0
}

// hasActiveTimedDefenseBuff reports whether p currently carries an unexpired
// TimedDefenseBuff for the given spell.
func hasActiveTimedDefenseBuff(p *Player, spellID int) bool {
	now := time.Now()
	for _, b := range p.TimedDefenseBuffs {
		if b.SpellID == spellID && now.Before(b.Expiry) {
			return true
		}
	}
	return false
}

// massProtectionGroupBonus shares Mass Protection's (spell 130) +25 defense with the
// whole group: if some other member of player's group currently carries an active
// Mass Protection buff, player gets the same +25 without needing (or being able to
// stack) a copy of their own. The caster's own copy is already counted via the flat
// += spell.DefBonus applyTimedDefenseBuff does on cast, so a player who already has
// their own active buff never gets a second +25 from a group-mate's — checked live
// against current group membership and buff expiry on every call, so leaving the
// group (either the beneficiary's or the original caster's) drops the bonus
// immediately with no separate cleanup needed.
func (e *GameEngine) massProtectionGroupBonus(player *Player) int {
	const massProtectionSpellID = 130
	if hasActiveTimedDefenseBuff(player, massProtectionSpellID) {
		return 0
	}
	for _, mate := range e.groupMates(player) {
		if hasActiveTimedDefenseBuff(mate, massProtectionSpellID) {
			return 25
		}
	}
	return 0
}

func playerDefenseRating(player *Player) int {
	rating := 25
	rating += player.Level * 3
	rating += player.Skills[6] * 5 // Dodge & Parry: +5 per rank
	rating += player.Agility / 5
	// Martial Arts defense bonus: +2 per rank if unarmed
	if player.Wielded == nil {
		rating += player.Skills[24] * 2
	}
	switch player.Stance {
	case StanceOffensive:
		rating -= 15
	case StanceDefensive:
		rating += 15
	case StanceBerserk:
		rating -= 25
	case StanceWary:
		rating += 5
	}
	rating += player.DefenseBonus
	switch player.Position {
	case 1:
		rating -= 15
	case 2:
		rating -= 25
	case 3:
		rating -= 10
	}
	return rating
}

// armorSlotsForBodyPart returns which WornSlot(s) protect a given rolled hit location.
// humanLocationWeights (wounds.go, what rollBodyPart actually draws from for players) has
// a genuine, separate "right hand"/"left hand" location distinct from arms — so gauntlets
// (WORN_HANDS) protect hand hits exclusively, no stacking with the torso piece. There is
// no separate foot location, though (legs are the lowest human extremity), so boots
// (WORN_FEET2, plus the new Thigh Guards' WORN_TRUNK2 — ITEMSNEW.SCR 9145) still stack
// with the torso piece on leg hits — the torso piece explicitly covers torso+arms+legs as
// one combined piece per the armor framework's design (a cuirass has sleeves/greaves, not
// just a breastplate), and leg hits have nothing more specific than that to fall back on
// the way hand hits now do. WORN_BACK (Shoulder Guards, ITEM3.SCR 560, pre-dates this
// framework) stacks onto "back" specifically, the one human location the torso piece alone
// previously left otherwise identical to "body".
func armorSlotsForBodyPart(part string) []string {
	switch part {
	case "head":
		return []string{"WORN_HEAD"}
	case "right hand", "left hand":
		return []string{"WORN_HANDS"}
	case "right leg", "left leg":
		return []string{"WORN_ARMOR", "WORN_FEET2", "WORN_TRUNK2"}
	case "back":
		return []string{"WORN_ARMOR", "WORN_BACK"}
	default: // "body", "right arm", "left arm"
		return []string{"WORN_ARMOR"}
	}
}

// armorDRForBodyPart returns the damage-reduction percentage from the player's worn
// armor that actually covers the given hit location — replacing the old flat
// playerArmorPercent, which summed every worn ARMOR item regardless of which body part
// was hit (so a helmet used to reduce leg damage too). Multiple pieces covering the same
// location (e.g. a torso piece's sleeves plus separate gauntlets on an arm hit) stack
// additively, same as the old flat sum did for everything; still capped at 85% and
// still floored by Drakin's natural armor.
func armorDRForBodyPart(player *Player, items map[int]*gameworld.ItemDef, part string) int {
	slots := armorSlotsForBodyPart(part)
	total := 0
	for _, worn := range player.Worn {
		def := items[worn.Archetype]
		if def == nil || def.Type != "ARMOR" {
			continue
		}
		if containsString(slots, def.WornSlot) {
			total += def.Parameter1
		}
	}
	if total > 85 {
		total = 85
	}
	if natural := drakinNaturalArmorPercent(player); natural > total {
		total = natural
	}
	return total
}

// drakinNaturalArmorPercent returns 0 for every race except Drakin. LEGENDS.DOC says
// Drakin never wear armor, and separately that "some races have natural armor that
// increases in effectiveness with every level" — undocumented for Drakin specifically
// (no numeric value survives anywhere in the source material), so this is a judgment
// call: scale from a modest 17% at level 1 up to a plate-armor-equivalent 75% (the
// strongest ordinary, non-unique body armor in the item scripts) by level 30, then hold
// there. Takes the max against any worn armor rather than stacking, since Drakin scales
// are their armor system, not a supplement to gear — nothing currently stops a Drakin
// from wearing armor items too (ITEMARM.SCR has no race restriction), so this avoids
// double-dipping if that ever happens.
func drakinNaturalArmorPercent(player *Player) int {
	if player.Race != RaceDrakin {
		return 0
	}
	pct := 15 + player.Level*2
	if pct > 75 {
		pct = 75
	}
	return pct
}

// ---- Damage Calculation ----

func playerDamage(player *Player, weaponDef *gameworld.ItemDef) int {
	if weaponDef == nil {
		if player.WolfForm {
			// Wolf form: claw/bite — higher base damage
			return rand.Intn(8) + 3 + player.Strength/10
		}
		// Martial Arts: +1 base damage per rank, +1 max damage per 2 ranks
		maSkill := player.Skills[24]
		baseDmg := rand.Intn(3+maSkill/2) + 1 + maSkill + player.Strength/20
		return baseDmg
	}
	maxDmg := weaponDef.Parameter1
	if maxDmg <= 0 {
		maxDmg = 3
	}
	dmg := rand.Intn(maxDmg) + 1
	if weaponDef.Type == "BOW_WEAPON" || weaponDef.Type == "THROWN_WEAPON" {
		dmg += player.Agility / 10
	} else {
		dmg += player.Strength / 10
	}
	if player.Stance == StanceBerserk {
		dmg = dmg * 12 / 10
	}
	// Backstab: 2x damage + 1 per Backstab rank. LEGENDS.DOC only says damage
	// "increases by level of backstab"; the 2x keeps results in line with
	// log0529.txt, where a steel rapier backstab did 29-38 vs 9 for a thrust.
	if player.BackstabNext {
		backstabSkill := player.Skills[2] // Backstab skill
		dmg = dmg*2 + backstabSkill
	}
	return dmg
}

// weaponCritDamage checks VAL3 for elemental crit or slayer bonus.
// Returns (extra damage, crit type description, hit).
func weaponCritDamage(wielded *InventoryItem, weaponDef *gameworld.ItemDef, monDef *gameworld.MonsterDef) (int, string) {
	if wielded == nil || weaponDef == nil {
		return 0, ""
	}
	val3 := wielded.Val3
	if val3 == 0 {
		val3 = weaponDef.Parameter3
	}
	if val3 == 0 {
		return 0, ""
	}
	val5 := wielded.Val5
	if val5 == 0 {
		// Infer crit max from weapon damage
		val5 = weaponDef.Parameter1 / 2
		if val5 < 5 {
			val5 = 5
		}
	}

	// Elemental crits (VAL3 2-18): chance-based extra damage
	switch {
	case val3 >= 2 && val3 <= 18:
		chance := 0
		dmgType := ""
		switch val3 {
		case 2:
			chance, dmgType = 50, "heat"
		case 3:
			chance, dmgType = 50, "cold"
		case 4:
			chance, dmgType = 40, "electric"
		case 5:
			chance, dmgType = 40, "heat"
		case 6:
			chance, dmgType = 40, "cold"
		case 7:
			chance, dmgType = 40, "electric"
		case 10:
			chance, dmgType = 30, "heat"
		case 11:
			chance, dmgType = 30, "cold"
		case 12:
			chance, dmgType = 30, "electric"
		case 13:
			chance, dmgType = 20, "heat"
		case 14:
			chance, dmgType = 20, "cold"
		case 15:
			chance, dmgType = 20, "electric"
		case 16:
			chance, dmgType = 10, "heat"
		case 17:
			chance, dmgType = 10, "cold"
		case 18:
			chance, dmgType = 10, "electric"
		}
		if chance > 0 && rand.Intn(100) < chance {
			extra := rand.Intn(val5) + 1
			// Apply elemental immunity
			if monDef != nil {
				immType := elementalImmunityType(dmgType)
				if level, ok := monDef.Immunities[immType]; ok {
					extra = applyImmunity(extra, level)
				}
			}
			typeNames := map[string]string{"heat": "fire", "cold": "cold", "electric": "lightning"}
			return extra, typeNames[dmgType]
		}

	// Slayer weapons (VAL3 21-32): bonus damage vs specific monster races
	case val3 >= 21 && val3 <= 32:
		if monDef != nil && monDef.Race == val3 {
			// Slayer hit! Double damage from val5
			return val5, "slayer"
		}
	}
	return 0, ""
}

// weaponPoisonLevel checks VAL4 for poison (51-100 = poison level 1-50).
func weaponPoisonLevel(wielded *InventoryItem) int {
	if wielded == nil {
		return 0
	}
	if wielded.Val4 >= 51 && wielded.Val4 <= 100 {
		return wielded.Val4 - 50
	}
	return 0
}

func monsterDamage(def *gameworld.MonsterDef) int {
	return monsterDamageForAttack(def.Attack1)
}

// monsterDamageForAttack is monsterDamage's formula parameterized on an attack rating
// instead of a def, so callers can pass an effective (buffed) rating — see
// monsterEffectiveAttack — instead of the monster's raw def.Attack1.
func monsterDamageForAttack(atk int) int {
	minDmg := max(1, atk/20)
	maxDmg := max(2, atk/5)
	if maxDmg <= minDmg {
		maxDmg = minDmg + 1
	}
	return rand.Intn(maxDmg-minDmg+1) + minDmg
}

// legBodyParts are the GMSCRIPT.DOC PART # values (0-19 body-part table) that
// represent a leg or paw — the things an "excellent hit" would need to knock
// out from under a creature to stagger it PART "off its feet."
var legBodyParts = map[int]bool{
	7: true, 8: true, 9: true, 10: true, 11: true, 12: true, 18: true, 19: true,
}

// monsterHasLegs reports whether a monster's anatomy includes at least one leg.
// Per GMSCRIPT.DOC, PART "can be used in conjunction with or alone": combined
// with a BodyType macro (HUMAN/ANIMAL/AVINE) it only ADDS parts on top of the
// macro's implied legged base (e.g. astral stalker is ANIMAL + PART 15/16/17/3
// for wings/tail/second-head, still four-legged); given alone with no macro,
// it's the creature's complete, authoritative anatomy (e.g. rattlesnake is
// PART 0/17 only — head and tail, no legs). PART values outside the documented
// 0-19 table (some older monster defs reuse the field inconsistently, e.g. a
// stray "PART 21") aren't real anatomy and are ignored. A monster with no
// BodyType and no in-range PART data at all defaults to having legs, matching
// prior (unconditional) behavior for the majority of monster defs that never
// bothered to tag either.
func monsterHasLegs(def *gameworld.MonsterDef) bool {
	if def.BodyType == "HUMAN" || def.BodyType == "ANIMAL" || def.BodyType == "AVINE" {
		return true
	}
	sawStandardPart := false
	for _, p := range def.Parts {
		if p < 0 || p > 19 {
			continue
		}
		sawStandardPart = true
		if legBodyParts[p] {
			return true
		}
	}
	return !sawStandardPart
}

// backBodyPart is GMSCRIPT.DOC's PART 2 ("back").
const backBodyPart = 2

// monsterHasBack reports whether a monster has a back to BACKSTAB. Same anatomy
// rules as monsterHasLegs: a HUMAN/ANIMAL/AVINE macro implies a back (the
// captures show alligators and bats being backstabbed); PART given alone is the
// creature's complete anatomy, so it needs PART 2; no anatomy at all gets the
// benefit of the doubt.
func monsterHasBack(def *gameworld.MonsterDef) bool {
	if def.BodyType == "HUMAN" || def.BodyType == "ANIMAL" || def.BodyType == "AVINE" {
		return true
	}
	sawStandardPart := false
	for _, p := range def.Parts {
		if p < 0 || p > 19 {
			continue
		}
		sawStandardPart = true
		if p == backBodyPart {
			return true
		}
	}
	return !sawStandardPart
}

// monsterCanBeKnockedDown reports whether a monster can realistically be
// knocked off its feet by a melee excellent hit — it needs legs to lose, and
// it can't be too massive to topple. Giants and dragons (RACE 27/31) and
// anything with Body (hit points) at or above 2000 — a natural gap in the
// monster roster between ordinary creatures (mostly well under 1000) and a
// handful of epic-scale ones (worms, krakens, elder horrors) — are treated as
// too large, matching the player-facing guidance that very large creatures
// shouldn't be easily knocked down.
// monsterIsDown reports whether a monster is already on the ground — knocked
// down (and not yet past KnockedDownExpiry), asleep, or still rising from sleep —
// and so can't be knocked off its feet again until it's standing.
func monsterIsDown(inst *MonsterInstance) bool {
	if inst == nil {
		return false
	}
	if inst.KnockedDown && time.Now().Before(inst.KnockedDownExpiry) {
		return true
	}
	return inst.Sleeping || inst.SleepStand
}

func monsterCanBeKnockedDown(def *gameworld.MonsterDef) bool {
	if !monsterHasLegs(def) {
		return false
	}
	if def.Race == 27 || def.Race == 31 {
		return false
	}
	if def.Body >= 2000 {
		return false
	}
	return true
}

func applyArmor(dmg, armorPct int) int {
	reduced := dmg * (100 - armorPct) / 100
	if reduced < 0 {
		reduced = 0
	}
	return reduced
}

// applyDrakinElementalVulnerability applies Drakin's extra heat/cold damage. LEGENDS.DOC
// says "great swings in temperature are more deadly to them than other races" but no
// numeric value survives anywhere in the source material, so +25% is a judgment call.
// dmgType is matched case-insensitively against "heat"/"cold" ("Heat"/"Cold" for monster
// special attacks, "heat"/"cold" for spell-derived damage types); anything else, or any
// non-Drakin player, is a no-op.
func applyDrakinElementalVulnerability(player *Player, dmgType string, dmg int) int {
	if player.Race != RaceDrakin {
		return dmg
	}
	switch strings.ToLower(dmgType) {
	case "heat", "cold":
		return dmg * 125 / 100
	}
	return dmg
}

func applyImmunity(dmg int, immunityLevel int) int {
	switch immunityLevel {
	case 0:
		return 0
	case 1:
		return dmg / 2
	case 3:
		return dmg * 3 / 2
	case 4:
		return dmg * 2
	default:
		return dmg
	}
}

// ---- MAGICWEAPON check ----
// Some monsters require magic weapons: 1=any magic, 2=bonus>=10, 3=bonus>=21

func checkMagicWeapon(player *Player, wielded *InventoryItem, weaponDef *gameworld.ItemDef, monDef *gameworld.MonsterDef) bool {
	if monDef.MagicWeapon <= 0 {
		return true // no requirement
	}
	// Martial Arts / Natural Weapons: rank 10+ bypasses level 1 (any magic),
	// 20+ bypasses level 2, 30+ bypasses level 3
	if wielded == nil {
		unarmedSkill := player.Skills[24] // Martial Arts
		if nw := player.Skills[4]; nw > unarmedSkill {
			unarmedSkill = nw // Natural Weapons
		}
		switch {
		case unarmedSkill >= 30 && monDef.MagicWeapon <= 3:
			return true
		case unarmedSkill >= 20 && monDef.MagicWeapon <= 2:
			return true
		case unarmedSkill >= 10 && monDef.MagicWeapon <= 1:
			return true
		}
	}
	if wielded == nil || weaponDef == nil {
		return false // unarmed can't hit magic-required monsters
	}
	bonus := wielded.Val2 // VAL2 = magic bonus
	switch monDef.MagicWeapon {
	case 1:
		return bonus > 0
	case 2:
		return bonus >= 10
	case 3:
		return bonus >= 21
	}
	return true
}

// ---- Body parts ----

var bodyParts = []string{"head", "body", "right arm", "left arm", "right leg", "left leg", "back"}
var animalParts = []string{"head", "body", "right foreleg", "left foreleg", "right hind leg", "left hind leg", "tail"}

func randomBodyPart(bodyType string) string {
	if bodyType == "ANIMAL" || bodyType == "AVINE" {
		return animalParts[rand.Intn(len(animalParts))]
	}
	return bodyParts[rand.Intn(len(bodyParts))]
}

// ---- Weapon helpers ----

func weaponImmunityType(weaponDef *gameworld.ItemDef) int {
	if weaponDef == nil {
		return 1
	}
	switch weaponDef.Type {
	case "CRUSH_WEAPON", "BLUNT_WEAPON":
		return 1
	default:
		return 2
	}
}

func (e *GameEngine) weaponDisplayName(player *Player, weaponDef *gameworld.ItemDef) string {
	if weaponDef == nil {
		if player.WolfForm {
			return "claws"
		}
		return "fists"
	}
	// Return name WITHOUT article — caller adds "your" prefix
	if player.Wielded != nil {
		return e.formatItemNameNoArticle(weaponDef, player.Wielded.Adj1, player.Wielded.Adj2, player.Wielded.Adj3, player.Wielded.Tail)
	}
	return strings.ToLower(e.nouns[weaponDef.NameID])
}

func (e *GameEngine) monsterWeaponName(def *gameworld.MonsterDef) string {
	if len(def.Weapons) > 0 {
		wep := e.items[def.Weapons[0].Archetype]
		if wep != nil {
			adj := ""
			if def.Weapons[0].Adj > 0 {
				if a, ok := e.adjectives[def.Weapons[0].Adj]; ok {
					adj = a + " "
				}
			}
			return adj + strings.ToLower(e.nouns[wep.NameID])
		}
	}
	return "claws"
}

// ---- Arena check ----

func (e *GameEngine) isArenaRoom(roomNum int) bool {
	room := e.rooms[roomNum]
	if room == nil {
		return false
	}
	for _, mod := range room.Modifiers {
		if mod == "ARENA" {
			return true
		}
	}
	return false
}

// ---- Player attacks Monster ----

func (e *GameEngine) doAttackMonster(ctx context.Context, player *Player, target string) *CommandResult {
	if player.Dead {
		return &CommandResult{Messages: []string{"You can't do that. You are dead."}}
	}
	if player.Stunned {
		return &CommandResult{Messages: []string{"You are stunned and cannot attack!"}}
	}
	if player.Immobilized {
		return &CommandResult{Messages: []string{"You are rooted to the spot!"}}
	}
	if player.IsImprisoned() {
		return &CommandResult{Messages: []string{"You are trapped within a blue force bubble and cannot attack!"}}
	}
	if msg := formActionBlockMessage(player); msg != "" {
		return &CommandResult{Messages: []string{msg}}
	}
	if player.Position == 2 {
		return &CommandResult{Messages: []string{"You can't attack while laying down! Stand up first."}}
	}

	if player.RoundTimeExpiry.After(time.Now()) {
		remaining := int(player.RoundTimeExpiry.Sub(time.Now()).Seconds()) + 1
		return &CommandResult{Messages: []string{fmt.Sprintf("[Wait %d seconds...]", remaining)}}
	}

	inst, def := e.findMonsterInRoom(player, target)
	if inst == nil {
		// Check if they're trying to attack a player
		if e.sessions != nil {
			for _, p := range e.sessions.OnlinePlayers() {
				if p.RoomNumber == player.RoomNumber && strings.HasPrefix(strings.ToUpper(p.FirstName), strings.ToUpper(target)) {
					if player.IsGM {
						return &CommandResult{Messages: []string{fmt.Sprintf("[GM combat with players is not yet implemented. %s is here.]", p.FirstName)}}
					}
					return &CommandResult{Messages: []string{"Player combat is not allowed here."}}
				}
			}
		}
		return &CommandResult{Messages: []string{fmt.Sprintf("You don't see '%s' here to attack.", target)}}
	}

	// Check if a guard monster intervenes
	guardInst, guardDef := e.findGuardFor(inst, player.RoomNumber)
	if guardInst != nil && guardDef != nil {
		guardName := FormatMonsterName(guardDef, e.monAdjs)
		guardArticle := articleFor(guardName, guardDef.Unique)
		if e.sendToPlayer != nil {
			e.sendToPlayer(player.FirstName, []string{fmt.Sprintf("%s%s is now guarding %s%s.", capArticle(guardArticle), guardName, articleFor(FormatMonsterName(def, e.monAdjs), def.Unique), FormatMonsterName(def, e.monAdjs))})
		}
		// Redirect attack to the guard
		inst = guardInst
		def = guardDef
	}

	name := FormatMonsterName(def, e.monAdjs)
	article := articleFor(name, def.Unique)

	// Imprison (231): the force bubble protects the target from being attacked
	// as well as attacking — per MAGIC.TXT, "prevents them from attacking or
	// being attacked."
	if inst.Imprisoned {
		return &CommandResult{Messages: []string{fmt.Sprintf("A shimmering force bubble protects %s%s -- your attack has no effect!", article, name)}}
	}

	backstab := player.BackstabNext
	if backstab && !monsterHasBack(def) {
		return &CommandResult{Messages: []string{fmt.Sprintf("%s%s has no back to stab!", capArticle(article), name)}}
	}

	weaponDef := e.currentWeaponDef(player)

	// Check ranged weapon is loaded
	isRangedWeapon := weaponDef != nil && (weaponDef.Type == "BOW_WEAPON" || weaponDef.Type == "HANDGUN" || weaponDef.Type == "RIFLE")
	if isRangedWeapon && (player.Wielded == nil || player.Wielded.Val3 <= 0) {
		return &CommandResult{Messages: []string{fmt.Sprintf("Your %s is not loaded! Use NOCK or LOAD first.", strings.ToLower(e.nouns[weaponDef.NameID]))}}
	}

	// Check MAGICWEAPON requirement
	if !checkMagicWeapon(player, player.Wielded, weaponDef, def) {
		texI := def.TextOverrides["TEXI"]
		if texI == "" {
			texI = fmt.Sprintf("Your weapon is not powerful enough to affect %s%s.", article, name)
		}
		return &CommandResult{Messages: []string{texI}}
	}

	// Melee-range weapons (bare hands and anything without reach/thrown/fired
	// range — see weaponNeedsAdvance) require the attacker to have ADVANCEd on
	// the target first, unless the target has already closed to melee with them.
	// "Closed to melee" covers two cases: inst.Target == player.FirstName (set
	// by monster aggro as well as ATTACK, so an aggro'd player can always fight
	// back without ADVANCEing themselves), or the player is guarding whoever
	// inst.Target actually is — monsterAttackPlayer transparently redirects the
	// attack to the guard (GuardTargets, see doGuard) without ever updating
	// inst.Target itself, so a guard who's visibly being swung at ("X steps
	// forward in defense of Y!" / "A mound swings at X...") would otherwise still
	// be told they haven't closed to melee range.
	weaponType := ""
	if weaponDef != nil {
		weaponType = weaponDef.Type
	}
	engagedByMonster := inst.Target == player.FirstName ||
		(inst.Target != "" && containsString(player.GuardTargets, inst.Target))
	if weaponNeedsAdvance(weaponType, weaponDef != nil) && !engagedByMonster {
		advanced := containsInt(player.AdvancedOnMonsters, inst.ID)
		if !advanced && backstab {
			// A backstab closes to melee as part of the strike — no separate
			// "advances towards" line in the captures, just reveal-then-stab.
			player.AdvancedOnMonsters = append(player.AdvancedOnMonsters, inst.ID)
			advanced = true
		}
		if !advanced {
			return &CommandResult{Messages: []string{fmt.Sprintf("You haven't closed to melee range with %s%s! Try ADVANCE first.", article, name)}}
		}
	}

	// Engage
	if backstab {
		// Committed: leave hiding only now, so a backstab refused above
		// (no back, not advanced, in round time...) keeps the player hidden.
		// doBackstab adds the reveal messages.
		player.Hidden = false
		player.Invisible = false
		player.PhantomForm = false
	}
	e.breakCarryAsCarrier(ctx, player)
	player.CombatTarget = &CombatTarget{IsMonster: true, MonsterID: inst.ID}
	player.Joined = true
	e.monsterMgr.mu.Lock()
	for i := range e.monsterMgr.instances {
		if e.monsterMgr.instances[i].ID == inst.ID {
			if e.monsterMgr.instances[i].Target == "" {
				e.monsterMgr.instances[i].Target = player.FirstName
			}
			break
		}
	}
	e.monsterMgr.mu.Unlock()

	// Cry for law (strategy 1-25 or 101-125)
	if (def.Strategy >= 1 && def.Strategy <= 25) || (def.Strategy >= 101 && def.Strategy <= 125) {
		e.cryForLaw(player, inst, def)
	}

	// Two-weapon setup: check off-hand weapon for Two Weapons skill
	var offHandDef *gameworld.ItemDef
	isTwoWeapon := false
	if player.OffHand != nil {
		offHandDef = e.items[player.OffHand.Archetype]
		if offHandDef != nil && isWeapon(offHandDef.Type) && player.Skills[1] > 0 {
			isTwoWeapon = true
		}
	}

	// Fatigue drain for melee attacks (not ranged)
	isRanged := weaponDef != nil && (weaponDef.Type == "BOW_WEAPON" || weaponDef.Type == "THROWN_WEAPON")
	if !isRanged {
		fatCost := 1
		if weaponDef != nil && weaponDef.Weight > 5 {
			fatCost = weaponDef.Weight / 7 // reduced from /5 to cap heavy weapon fatigue
			if fatCost > 3 {
				fatCost = 3
			}
		}
		if weaponDef != nil {
			fatCost -= e.weaponSpecializationRank(player, weaponDef)
			if fatCost < 1 {
				fatCost = 1
			}
		}
		if isTwoWeapon {
			ohFat := 1
			if offHandDef.Weight > 5 {
				ohFat = offHandDef.Weight / 7
				if ohFat > 3 {
					ohFat = 3
				}
			}
			ohFat -= e.weaponSpecializationRank(player, offHandDef)
			if ohFat < 1 {
				ohFat = 1
			}
			fatCost += ohFat
		}
		player.Fatigue -= fatCost
		if player.Fatigue < 0 {
			player.Fatigue = 0
		}
		if player.Fatigue <= 0 {
			return &CommandResult{Messages: []string{"You are too fatigued to attack!"}}
		}
	}

	// Apply weather modifier — Resist Weather (506) cancels it entirely
	wMod := e.weatherMod(player.RoomNumber)
	if !player.ResistWeatherExpiry.IsZero() && time.Now().Before(player.ResistWeatherExpiry) {
		wMod = 0
	}

	// Fatigue penalty to ToHit
	fatPenalty := 0
	if player.MaxFatigue > 0 {
		fatRatio := player.Fatigue * 100 / player.MaxFatigue
		if fatRatio < 25 {
			fatPenalty = 25 // under 1/4 fatigue: -25
		} else if fatRatio < 50 {
			fatPenalty = 10 // under 1/2 fatigue: -10
		}
	}

	// Resolve to-hit
	attackRating := e.playerAttackRating(player, weaponDef) + wMod - fatPenalty
	if inst.Stunned {
		attackRating += 20 // bonus for attacking a stunned target (LEGENDS.DOC: -20 defense while stunned)
	}
	if inst.KnockedDown {
		attackRating += 50 // bonus for attacking a knocked-down target (LEGENDS.DOC: -50 defense while down)
	}
	monDefense := monsterEffectiveDefense(def, inst)
	toHit := calcToHit(attackRating, monDefense)
	roll := rand.Intn(100) + 1

	var selfVerb, thirdVerb, dmgNoun string
	if weaponDef == nil && player.WolfForm {
		selfVerb, thirdVerb, dmgNoun = "claw", "claws", "claw"
	} else {
		selfVerb, thirdVerb, dmgNoun = attackVerb(weaponDef)
	}
	if backstab {
		selfVerb, thirdVerb = "backstab", "backstabs"
	}
	weaponName := e.weaponDisplayName(player, weaponDef)

	result := &CommandResult{}
	var msgs []string

	msgs = append(msgs, fmt.Sprintf("You %s at %s%s with your %s.", selfVerb, article, name, weaponName))

	// Weapon clash on roll < 3 (only vs weapon-wielding monsters). Requires a real
	// wielded item — natural weapons (Claw Growth) have no InventoryItem instance to
	// damage/break, so they're exempt from clash entirely rather than clashing with
	// no visible effect.
	if roll < 3 && weaponDef != nil && player.Wielded != nil && len(def.Weapons) > 0 {
		// Agility helps the player deflect/pull the blow rather than take the clash
		// full-on, reducing the chance their own weapon takes the damage.
		weaponStr := e.weaponHardness(player, player.Wielded, weaponDef) + player.Agility/5
		clashRoll := rand.Intn(100) + rand.Intn(100) + 2
		msgs = append(msgs, fmt.Sprintf(" [ToHit: %d, Roll: %d] Weapon Clash! [Strength: %d, 2d100 Roll: %d]", toHit, roll, weaponStr, clashRoll))
		if clashRoll > weaponStr {
			if player.Wielded != nil && player.Wielded.State == "DAMAGED" {
				msgs = append(msgs, fmt.Sprintf(" Your %s breaks!", strings.ToLower(e.nouns[weaponDef.NameID])))
				player.Wielded = nil
			} else if player.Wielded != nil {
				player.Wielded.State = "DAMAGED"
				const damagedAdj = 83
				w := player.Wielded
				if w.Adj1 == 0 {
					w.Adj1 = damagedAdj
				} else if w.Adj2 == 0 {
					w.Adj2 = w.Adj1
					w.Adj1 = damagedAdj
				} else if w.Adj3 == 0 {
					w.Adj3 = w.Adj2
					w.Adj2 = w.Adj1
					w.Adj1 = damagedAdj
				}
				msgs = append(msgs, fmt.Sprintf(" %s damaged!", strings.Title(strings.ToLower(e.nouns[weaponDef.NameID]))))
				// Auto-unwield: players kept fighting with a damaged weapon without
				// noticing, breaking it on the next clash. Newly-damaged weapons are
				// sheathed automatically so continuing to fight bare-handed (or with a
				// swap) is a deliberate choice, not an accident.
				fullName := e.formatItemName(weaponDef, w.Adj1, w.Adj2, w.Adj3, w.Tail)
				player.Inventory = append(player.Inventory, *w)
				player.Wielded = nil
				msgs = append(msgs, fmt.Sprintf(" You quickly sheathe %s before it breaks completely!", fullName))
			}
		}
		result.Messages = msgs
		rtSec := applyRoundTime(player, 5)
		player.RoundTimeExpiry = time.Now().Add(time.Duration(rtSec) * time.Second)
		result.Messages = append(result.Messages, fmt.Sprintf("[Round: %d sec]", rtSec))
		if player.Hidden {
			player.Hidden = false
			result.Messages = append([]string{"You reveal yourself!"}, result.Messages...)
		}
		e.SavePlayer(ctx, player)
		result.PlayerState = player
		return result
	}

	// Damaged weapon penalty (-10 ToHit)
	if player.Wielded != nil && player.Wielded.State == "DAMAGED" {
		toHit += 10 // harder to hit with damaged weapon
	}

	if roll >= toHit {
		// A stunned or knocked-down target is defenseless — any successful hit against
		// one is an automatic critical (the same "Excellent Hit!"/open-ended bonus
		// damage path a natural 96+ roll gets), not just the to-hit bonus above.
		excellent := roll >= 96 || inst.Stunned || inst.KnockedDown
		hitLabel := "Hit!"
		if excellent {
			hitLabel = "Excellent Hit!"
		}
		// Open-ended roll: 96-100 adds a bonus roll
		openEndedBonus := 0
		if excellent {
			bonus := rand.Intn(100) + 1
			openEndedBonus = bonus / 2 // bonus damage %
			if bonus >= 96 {
				// Double open-ended!
				hitLabel = "Devastating Critical!!"
				openEndedBonus += rand.Intn(100)/2 + 50
			}
		}
		msgs = append(msgs, fmt.Sprintf(" [ToHit: %d, Roll: %d] %s", toHit, roll, hitLabel))

		dmg := playerDamage(player, weaponDef)
		if openEndedBonus > 0 {
			dmg = dmg * (100 + openEndedBonus) / 100
		}
		dmg = applyArmor(dmg, def.Armor)
		immType := weaponImmunityType(weaponDef)
		if level, ok := def.Immunities[immType]; ok {
			dmg = applyImmunity(dmg, level)
		}
		if dmg <= 0 {
			dmg = 1
		}

		specRank := e.weaponSpecializationRank(player, weaponDef)
		part, locMult := rollBodyPart(def.BodyType, specRank)
		if backstab {
			// Always the back, at the full vital-area multiplier — LEGENDS.DOC:
			// specialization "has no effect when combined with backstabbing,
			// because a backstab already strikes a vital area all the time."
			part, locMult = "back", 100
		}
		dmg = dmg * locMult / 100
		if dmg <= 0 {
			dmg = 1
		}
		dtype := damageTypeForWeapon(weaponDef, e)
		woundLevel := woundLevelFromDamage(dmg, inst.MaxHP)
		e.addMonsterWound(inst.ID, part, dtype, woundLevel)
		// Message construction deferred until after damageMonster below so a killing
		// blow with no elemental crit can show weaponKillFlavor instead of the normal
		// severity/damage line.
		baseHitLine := fmt.Sprintf(" %s %s to %s. [%d Damage]", damageSeverity(dmg, inst.MaxHP), dmgNoun, part, dmg)

		// Weapon elemental crit / slayer bonus. Message construction is deferred until
		// after damageMonster below so a killing crit can show its kill-flavor line
		// (see elementalKillFlavor) instead of the normal severity/damage line.
		var critType string
		var critMsgs []string
		if critDmg, ct := weaponCritDamage(player.Wielded, weaponDef, def); critDmg > 0 {
			critType = ct
			critPart, critLocMult := rollBodyPart(def.BodyType, specRank)
			critDmg = critDmg * critLocMult / 100
			if critDmg <= 0 {
				critDmg = 1
			}
			dmg += critDmg
			var critDtype string
			switch critType {
			case "fire", "cold", "lightning":
				critDtype = "burn"
			default:
				critDtype = damageTypeForWeapon(weaponDef, e)
			}
			critLevel := woundLevelFromDamage(critDmg, inst.MaxHP)
			e.addMonsterWound(inst.ID, critPart, critDtype, critLevel)
			critWord := damageSeverity(critDmg, inst.MaxHP)
			weaponNoun := strings.ToLower(e.nouns[weaponDef.NameID])
			switch critType {
			case "fire":
				critMsgs = append(critMsgs, fmt.Sprintf(" The %s radiates intense heat!", weaponNoun))
				critMsgs = append(critMsgs, fmt.Sprintf(" %s burn to %s. [%d Damage]", critWord, critPart, critDmg))
			case "cold":
				critMsgs = append(critMsgs, fmt.Sprintf(" The %s radiates intense cold!", weaponNoun))
				critMsgs = append(critMsgs, fmt.Sprintf(" %s freeze to %s. [%d Damage]", critWord, critPart, critDmg))
			case "lightning":
				critMsgs = append(critMsgs, fmt.Sprintf(" The %s crackles with electricity!", weaponNoun))
				critMsgs = append(critMsgs, fmt.Sprintf(" %s shock to %s. [%d Damage]", critWord, critPart, critDmg))
			case "slayer":
				critMsgs = append(critMsgs, " Your weapon resonates against its foe!")
				critMsgs = append(critMsgs, fmt.Sprintf(" %s strike to %s. [%d Damage]", critWord, critPart, critDmg))
			}
		}

		killed, woke := e.damageMonster(inst.ID, dmg, player.FirstName)

		// If there's no elemental crit to supply its own kill-flavor line below, a
		// killing base hit gets weaponKillFlavor in place of the normal severity line.
		// weaponDef is nil for bare-handed/Martial Arts combat, so it can't be
		// dereferenced directly here.
		if killed && critType == "" {
			ranged := weaponDef != nil && isRangedWeaponType(weaponDef.Type)
			if kf := weaponKillFlavor(dtype, ranged); kf != "" {
				baseHitLine = " " + kf
			}
		}
		msgs = append(msgs, baseHitLine)

		if len(critMsgs) > 0 {
			if killed {
				if kf := elementalKillFlavor(critType); kf != "" {
					msgs = append(msgs, critMsgs[0], " "+kf)
				} else {
					msgs = append(msgs, critMsgs...)
				}
			} else {
				msgs = append(msgs, critMsgs...)
			}
		}

		if woke {
			msgs = append(msgs, fmt.Sprintf("The %s wakes up, startled!", name))
		}

		// Weapon poison — no effect on undead (RACE 22), which lack a life force for it to act on.
		if poisonLvl := weaponPoisonLevel(player.Wielded); poisonLvl > 0 && !killed && def.Race != 22 {
			msgs = append(msgs, " Your weapon delivers its venom!")
		}

		wasStunned := false
		staggerBroadcast := ""
		if excellent && !killed {
			if rand.Intn(100) < 30 {
				wasStunned = true
				// LEGENDS.DOC: an excellent hit "may result in stunning you or knocking
				// you off your feet" — two distinct outcomes of the same crit, not the
				// same thing (see the MonsterInstance field comments). No documented
				// split between the two, so 50/50 is a judgment call.
				// inst here points into a copy returned by MonstersInRoom, so the flag
				// must be written back into the real instance slice by ID (see
				// castSunraySpell for the same pattern) or it never actually takes effect.
				// Undead (RACE 22) lack a nervous system for a crit to stun (GMSCRIPT.DOC
				// immunity #6, "nervous system attacks"), mirroring the poison-immunity
				// exemption above — they can still be knocked down, a physical outcome
				// (as long as they physically can be — see monsterCanBeKnockedDown).
				// A creature that can't be knocked down (no legs, or too large/heavy)
				// gets staggered by stun instead, so the excellent hit still lands some
				// effect on it rather than silently doing nothing.
				// Already on the ground (knocked down, or asleep/still rising from
				// Slumber) — can't be knocked off its feet again until it's back up,
				// so it's staggered by stun instead, like a creature with no legs.
				canKnockDown := monsterCanBeKnockedDown(def) && !monsterIsDown(inst)
				if def.Race != 22 && (rand.Intn(100) < 50 || !canKnockDown) {
					stunSecs := 3 + rand.Intn(4) // 3-6 sec; no melee-crit-stun duration is documented (only psi spells have one), so this is a judgment call too
					stunExpiry := time.Now().Add(time.Duration(stunSecs) * time.Second)
					e.monsterMgr.mu.Lock()
					for i := range e.monsterMgr.instances {
						if e.monsterMgr.instances[i].ID == inst.ID {
							e.monsterMgr.instances[i].Stunned = true
							e.monsterMgr.instances[i].StunExpiry = stunExpiry
							break
						}
					}
					e.monsterMgr.mu.Unlock()
					inst.Stunned = true
					inst.StunExpiry = stunExpiry
					msgs = append(msgs, " It is stunned!")
					staggerBroadcast = ", stun"
				} else if canKnockDown {
					knockSecs := 3 + rand.Intn(4) // 3-6 sec, same judgment call as Stunned above
					knockExpiry := time.Now().Add(time.Duration(knockSecs) * time.Second)
					e.monsterMgr.mu.Lock()
					for i := range e.monsterMgr.instances {
						if e.monsterMgr.instances[i].ID == inst.ID {
							e.monsterMgr.instances[i].KnockedDown = true
							e.monsterMgr.instances[i].KnockedDownExpiry = knockExpiry
							break
						}
					}
					e.monsterMgr.mu.Unlock()
					inst.KnockedDown = true
					inst.KnockedDownExpiry = knockExpiry
					msgs = append(msgs, " It is knocked off its feet!")
					staggerBroadcast = ", knockdown"
				}
				// else: undead (immune to stun) that also can't be knocked down (no
				// legs, or too large) — the excellent hit lands with no extra stagger
				// effect rather than an impossible one.
			}
		}

		if killed {
			deathText := def.TextOverrides["TEXD"]
			if deathText != "" {
				msgs = append(msgs, fmt.Sprintf(" It %s", deathText))
			} else {
				msgs = append(msgs, " It collapses, dead.")
			}
			e.handleMonsterDeath(e.sharedXPRecipients(player), inst, def)
		}

		// Build simplified 3rd-person broadcast
		broadcastMsg := fmt.Sprintf("%s %s at %s%s. %s %s", player.DisplayNameCap(), thirdVerb, article, name, hitLabel, simplifiedDamageTier(dmg))
		if wasStunned {
			broadcastMsg += staggerBroadcast
		}
		broadcastMsg += "."
		if killed {
			deathText := def.TextOverrides["TEXD"]
			if deathText != "" {
				broadcastMsg += fmt.Sprintf(" It %s", deathText)
			} else {
				broadcastMsg += " It collapses, dead."
			}
		}
		result.RoomBroadcast = []string{broadcastMsg}
	} else {
		msgs = append(msgs, fmt.Sprintf(" [ToHit: %d, Roll: %d] Miss.", toHit, roll))
		result.RoomBroadcast = []string{fmt.Sprintf("%s %s at %s%s. Miss.", player.DisplayNameCap(), thirdVerb, article, name)}
	}

	result.Messages = msgs

	// Two-weapon second swing (off-hand weapon, Two Weapons skill). On a
	// backstab, the follow-up is a backstab too if the off-hand is also a
	// puncture weapon (back, full vital damage, Backstab bonus via
	// playerDamage); otherwise it's an ordinary swing.
	player.BackstabNext = false
	if isTwoWeapon && player.CombatTarget != nil {
		ohBackstab := backstab && isPunctureWeapon(offHandDef)
		player.BackstabNext = ohBackstab
		defer func() { player.BackstabNext = false }()
		twoWepSkill := player.Skills[1]
		offHandSkillID := weaponSkillForType(offHandDef.Type)
		effectiveRank := player.Skills[offHandSkillID]
		if effectiveRank > twoWepSkill {
			effectiveRank = twoWepSkill
		}
		ohAttack := 50 + player.Level*3 + effectiveRank*5 + player.Strength/5
		ohAttack += player.OffHand.Sharpness + player.OffHand.Val2
		switch player.Stance {
		case StanceOffensive:
			ohAttack += 15
		case StanceDefensive:
			ohAttack -= 15
		case StanceBerserk:
			ohAttack += 25
		case StanceWary:
			ohAttack -= 5
		}
		ohAttack += wMod - fatPenalty
		ohToHit := calcToHit(ohAttack, monDefense)
		ohRoll := rand.Intn(100) + 1
		ohSelfVerb, ohThirdVerb, ohDmgNoun := attackVerb(offHandDef)
		if ohBackstab {
			ohSelfVerb, ohThirdVerb = "backstab", "backstabs"
		}
		ohWepName := e.formatItemNameNoArticle(offHandDef, player.OffHand.Adj1, player.OffHand.Adj2, player.OffHand.Adj3, player.OffHand.Tail)
		result.Messages = append(result.Messages, fmt.Sprintf("You %s at %s%s with your %s.", ohSelfVerb, article, name, ohWepName))
		if ohRoll >= ohToHit {
			// Same critical rule as the main-hand swing above: a natural 96+, or any hit
			// on a stunned or knocked-down target, is an excellent hit with the
			// open-ended bonus damage. The off-hand used to always print a plain "Hit!"
			// with no bonus — even right after the main hand had knocked the target down.
			ohExcellent := ohRoll >= 96 || inst.Stunned || inst.KnockedDown
			ohHitLabel := "Hit!"
			ohOpenEndedBonus := 0
			if ohExcellent {
				ohHitLabel = "Excellent Hit!"
				bonus := rand.Intn(100) + 1
				ohOpenEndedBonus = bonus / 2
				if bonus >= 96 {
					ohHitLabel = "Devastating Critical!!"
					ohOpenEndedBonus += rand.Intn(100)/2 + 50
				}
			}
			result.Messages = append(result.Messages, fmt.Sprintf(" [ToHit: %d, Roll: %d] %s", ohToHit, ohRoll, ohHitLabel))
			ohDmg := playerDamage(player, offHandDef)
			if ohOpenEndedBonus > 0 {
				ohDmg = ohDmg * (100 + ohOpenEndedBonus) / 100
			}
			ohDmg = applyArmor(ohDmg, def.Armor)
			ohImmType := weaponImmunityType(offHandDef)
			if level, ok := def.Immunities[ohImmType]; ok {
				ohDmg = applyImmunity(ohDmg, level)
			}
			if ohDmg <= 0 {
				ohDmg = 1
			}
			ohSpecRank := e.weaponSpecializationRank(player, offHandDef)
			ohPart, ohLocMult := rollBodyPart(def.BodyType, ohSpecRank)
			if ohBackstab {
				ohPart, ohLocMult = "back", 100
			}
			ohDmg = ohDmg * ohLocMult / 100
			if ohDmg <= 0 {
				ohDmg = 1
			}
			ohDtype := damageTypeForWeapon(offHandDef, e)
			ohWoundLevel := woundLevelFromDamage(ohDmg, inst.MaxHP)
			e.addMonsterWound(inst.ID, ohPart, ohDtype, ohWoundLevel)
			// Message construction deferred until after damageMonster below — see the
			// main-hand attack above.
			ohBaseHitLine := fmt.Sprintf(" %s %s to %s. [%d Damage]", damageSeverity(ohDmg, inst.MaxHP), ohDmgNoun, ohPart, ohDmg)

			// Weapon elemental crit / slayer bonus (off-hand). Message construction is
			// deferred until after damageMonster below — see the main-hand attack above.
			var ohCritType string
			var ohCritMsgs []string
			if ohCritDmg, oct := weaponCritDamage(player.OffHand, offHandDef, def); ohCritDmg > 0 {
				ohCritType = oct
				ohCritPart, ohCritLocMult := rollBodyPart(def.BodyType, ohSpecRank)
				ohCritDmg = ohCritDmg * ohCritLocMult / 100
				if ohCritDmg <= 0 {
					ohCritDmg = 1
				}
				ohDmg += ohCritDmg
				var ohCritDtype string
				switch ohCritType {
				case "fire", "cold", "lightning":
					ohCritDtype = "burn"
				default:
					ohCritDtype = damageTypeForWeapon(offHandDef, e)
				}
				ohCritLevel := woundLevelFromDamage(ohCritDmg, inst.MaxHP)
				e.addMonsterWound(inst.ID, ohCritPart, ohCritDtype, ohCritLevel)
				ohCritWord := damageSeverity(ohCritDmg, inst.MaxHP)
				ohWeaponNoun := strings.ToLower(e.nouns[offHandDef.NameID])
				switch ohCritType {
				case "fire":
					ohCritMsgs = append(ohCritMsgs, fmt.Sprintf(" The %s radiates intense heat!", ohWeaponNoun))
					ohCritMsgs = append(ohCritMsgs, fmt.Sprintf(" %s burn to %s. [%d Damage]", ohCritWord, ohCritPart, ohCritDmg))
				case "cold":
					ohCritMsgs = append(ohCritMsgs, fmt.Sprintf(" The %s radiates intense cold!", ohWeaponNoun))
					ohCritMsgs = append(ohCritMsgs, fmt.Sprintf(" %s freeze to %s. [%d Damage]", ohCritWord, ohCritPart, ohCritDmg))
				case "lightning":
					ohCritMsgs = append(ohCritMsgs, fmt.Sprintf(" The %s crackles with electricity!", ohWeaponNoun))
					ohCritMsgs = append(ohCritMsgs, fmt.Sprintf(" %s shock to %s. [%d Damage]", ohCritWord, ohCritPart, ohCritDmg))
				case "slayer":
					ohCritMsgs = append(ohCritMsgs, " Your weapon resonates against its foe!")
					ohCritMsgs = append(ohCritMsgs, fmt.Sprintf(" %s strike to %s. [%d Damage]", ohCritWord, ohCritPart, ohCritDmg))
				}
			}

			ohKilled, ohWoke := e.damageMonster(inst.ID, ohDmg, player.FirstName)

			if ohKilled && ohCritType == "" {
				if kf := weaponKillFlavor(ohDtype, isRangedWeaponType(offHandDef.Type)); kf != "" {
					ohBaseHitLine = " " + kf
				}
			}
			result.Messages = append(result.Messages, ohBaseHitLine)

			if len(ohCritMsgs) > 0 {
				if ohKilled {
					if kf := elementalKillFlavor(ohCritType); kf != "" {
						result.Messages = append(result.Messages, ohCritMsgs[0], " "+kf)
					} else {
						result.Messages = append(result.Messages, ohCritMsgs...)
					}
				} else {
					result.Messages = append(result.Messages, ohCritMsgs...)
				}
			}

			if ohWoke {
				result.Messages = append(result.Messages, fmt.Sprintf(" The %s wakes up, startled!", name))
			}

			// Weapon poison (off-hand) — no effect on undead (RACE 22).
			if ohPoisonLvl := weaponPoisonLevel(player.OffHand); ohPoisonLvl > 0 && !ohKilled && def.Race != 22 {
				result.Messages = append(result.Messages, " Your weapon delivers its venom!")
			}

			if ohKilled {
				result.Messages = append(result.Messages, " It collapses, dead.")
				e.handleMonsterDeath(e.sharedXPRecipients(player), inst, def)
			}
			result.RoomBroadcast = append(result.RoomBroadcast, fmt.Sprintf("%s %s at %s%s with their off hand. %s", player.DisplayNameCap(), ohThirdVerb, article, name, strings.TrimRight(ohHitLabel, "!")+"."))
		} else {
			result.Messages = append(result.Messages, fmt.Sprintf(" [ToHit: %d, Roll: %d] Miss.", ohToHit, ohRoll))
			result.RoomBroadcast = append(result.RoomBroadcast, fmt.Sprintf("%s %s at %s%s with their off hand. Miss.", player.DisplayNameCap(), ohThirdVerb, article, name))
		}
	}

	// Roundtime: base 5, reduced by quickness and Combat Maneuvering
	rtSeconds := 5
	if player.Quickness > 80 {
		rtSeconds = 3
	} else if player.Quickness > 50 {
		rtSeconds = 4
	}
	// Combat Maneuvering: -1 sec per rank (from skills.txt)
	combatManeuver := player.Skills[10]
	rtSeconds -= combatManeuver
	if player.Stance == StanceBerserk {
		rtSeconds--
	}
	if rtSeconds < 2 {
		rtSeconds = 2
	}
	rtSeconds = applyRoundTime(player, rtSeconds)
	player.RoundTimeExpiry = time.Now().Add(time.Duration(rtSeconds) * time.Second)
	result.Messages = append(result.Messages, fmt.Sprintf("[Round: %d sec]", rtSeconds))

	// Unload ranged weapon after firing
	if isRangedWeapon && player.Wielded != nil {
		player.Wielded.Val3 = 0 // unloaded
	}

	// Attacking always reveals you (hidden, invisible, or phantom form)
	if player.Hidden || player.Invisible || player.PhantomForm {
		player.Hidden = false
		player.Invisible = false
		player.PhantomForm = false
		result.Messages = append([]string{"You reveal yourself!"}, result.Messages...)
	}

	e.SavePlayer(ctx, player)
	result.PlayerState = player

	return result
}

// isPunctureWeapon reports whether a weapon can BACKSTAB — LEGENDS.DOC:
// "only with a puncture weapon such as a dagger or rapier."
func isPunctureWeapon(def *gameworld.ItemDef) bool {
	return def != nil && (def.Type == "PUNCTURE_WEAPON" || def.Type == "STABTHROWN")
}

// doBackstab handles BACKSTAB <target> from hiding (Backstab, skill #2). It is
// an ordinary attack (doAttackMonster, so to-hit comes from the weapon's own
// skill) with BackstabNext set, which makes it: auto-advance on the target,
// always strike the back at full vital damage, add Backstab's bonus damage
// (playerDamage), and refuse targets with no back (monsterHasBack). A puncture
// weapon in the off hand works with Two Weapons — it's swapped into the main
// hand for this one attack and the main weapon gives the follow-up swing.
func (e *GameEngine) doBackstab(ctx context.Context, player *Player, target string) *CommandResult {
	if player.Skills[2] <= 0 {
		return &CommandResult{Messages: []string{"You haven't been trained in backstabbing."}}
	}
	var mainDef, offDef *gameworld.ItemDef
	if player.Wielded != nil {
		mainDef = e.items[player.Wielded.Archetype]
	}
	if player.OffHand != nil {
		offDef = e.items[player.OffHand.Archetype]
	}
	swapped := false
	if !isPunctureWeapon(mainDef) {
		if !isPunctureWeapon(offDef) || player.Skills[1] <= 0 {
			return &CommandResult{Messages: []string{"You can only backstab with a puncture weapon such as a dagger or rapier."}}
		}
		player.Wielded, player.OffHand = player.OffHand, player.Wielded
		swapped = true
	}

	player.BackstabNext = true
	result := e.doAttackMonster(ctx, player, target)
	player.BackstabNext = false
	if swapped {
		// Swapping back is right even if the attack broke or sheathed the
		// puncture weapon (Wielded nil): the other weapon returns to the main hand.
		player.Wielded, player.OffHand = player.OffHand, player.Wielded
	}

	// doAttackMonster clears Hidden only once the attack is committed; a refusal
	// (no back, round time, target not found) leaves the player hidden.
	if !player.Hidden {
		result.Messages = append([]string{"You reveal yourself."}, result.Messages...)
		result.RoomBroadcast = append([]string{fmt.Sprintf("%s reveals %sself.", player.DisplayNameCap(), player.Objective())}, result.RoomBroadcast...)
	}
	return result
}

// elementalGuardIntercept checks whether a summoned elemental is guarding player and intercepts
// the attack from attackerInst. Returns (intercepted, roomMessages, summonerNameToStun).
// summonerNameToStun is non-empty when the elemental was destroyed; caller handles stun.
// Safe to call without monsterMgr.mu held.
func (e *GameEngine) elementalGuardIntercept(_ *MonsterInstance, attackerDef *gameworld.MonsterDef, player *Player) (bool, []string, string) {
	e.monsterMgr.mu.Lock()
	var guardIdx int = -1
	for i := range e.monsterMgr.instances {
		g := &e.monsterMgr.instances[i]
		if g.IsSummoned && g.Alive && g.RoomNumber == player.RoomNumber && containsString(g.GuardingPlayers, player.FirstName) {
			guardIdx = i
			break
		}
	}
	if guardIdx < 0 {
		e.monsterMgr.mu.Unlock()
		return false, nil, ""
	}

	g := &e.monsterMgr.instances[guardIdx]
	gDef := e.monsters[g.DefNumber]
	if gDef == nil {
		e.monsterMgr.mu.Unlock()
		return false, nil, ""
	}

	gName := strings.ToLower(FormatMonsterName(gDef, e.monAdjs))
	gArticle := articleFor(gName, gDef.Unique)
	attName := strings.ToLower(FormatMonsterName(attackerDef, e.monAdjs))
	attArticle := articleFor(attName, attackerDef.Unique)

	monVerb, _ := monsterAttackVerb(attackerDef, e.items)
	monWeapon := e.monsterWeaponName(attackerDef)
	attackLine := fmt.Sprintf("%s%s %s %s%s with its %s.", capArticle(attArticle), attName, monVerb, gArticle, gName, monWeapon)

	// Roll to-hit against the elemental's defense first.
	toHit := calcToHit(attackerDef.Attack1, monsterEffectiveDefense(gDef, g))
	roll := rand.Intn(100) + 1
	summonerName := g.SummonerName

	hitLabel := "Hit!"
	if roll >= 96 {
		hitLabel = "Excellent Hit!"
	}

	if roll < toHit {
		e.monsterMgr.mu.Unlock()
		return true, []string{attackLine, fmt.Sprintf("[ToHit: %d, Roll: %d] Miss!", toHit, roll)}, ""
	}

	// Hit — check MAGICWEAPON immunity before applying damage.
	if gDef.MagicWeapon > 0 && attackerDef.WeaponPlus < gDef.MagicWeapon {
		texI := gDef.TextOverrides["TEXI"]
		if texI == "" {
			texI = fmt.Sprintf("%s%s's attack has no effect on %s%s.", capArticle(attArticle), attName, gArticle, gName)
		}
		e.monsterMgr.mu.Unlock()
		return true, []string{attackLine, fmt.Sprintf("[ToHit: %d, Roll: %d] %s", toHit, roll, hitLabel), texI}, ""
	}

	dmg := monsterDamage(attackerDef)
	dmg = applyArmor(dmg, gDef.Armor)
	var attackerWeaponDef *gameworld.ItemDef
	if len(attackerDef.Weapons) > 0 {
		attackerWeaponDef = e.items[attackerDef.Weapons[0].Archetype]
	}
	if level, ok := gDef.Immunities[weaponImmunityType(attackerWeaponDef)]; ok {
		dmg = applyImmunity(dmg, level)
	}
	if dmg <= 0 {
		dmg = 1
	}
	g.CurrentHP -= dmg

	hitLine := fmt.Sprintf("[ToHit: %d, Roll: %d] %s %s%s takes %d damage.", toHit, roll, hitLabel, gArticle, gName, dmg)

	if g.CurrentHP <= 0 {
		g.Alive = false
		g.DeathTime = time.Now()
		g.GuardingPlayers = nil
		texD := gDef.TextOverrides["TEXD"]
		var deathSuffix string
		if texD != "" {
			deathSuffix = fmt.Sprintf(" It %s", texD)
		} else {
			deathSuffix = " It is destroyed!"
		}
		e.monsterMgr.mu.Unlock()
		return true, []string{attackLine, hitLine + deathSuffix}, summonerName
	}

	e.monsterMgr.mu.Unlock()
	return true, []string{attackLine, hitLine}, ""
}

// ---- Monster attacks Player ----

// monsterAttackPlayer resolves a monster's attack against player. The returned
// defender is whoever actually took the attack — it differs from player when
// the attack is redirected to a guard, and callers must send playerMsgs (which
// contains the private [ToHit/Roll] detail) to defender, not to player.
func (e *GameEngine) monsterAttackPlayer(inst *MonsterInstance, def *gameworld.MonsterDef, player *Player) (playerMsgs []string, roomMsgs []string, defender *Player) {
	if player.Dead || !inst.Alive {
		return nil, nil, nil
	}

	// Guard redirect: if someone is guarding this player, redirect the attack
	if e.sessions != nil {
		for _, guard := range e.sessions.OnlinePlayers() {
			if guard.RoomNumber == player.RoomNumber && !guard.Dead && containsString(guard.GuardTargets, player.FirstName) {
				guardMsg := fmt.Sprintf("%s steps forward in defense of %s!", guard.DisplayNameCap(), player.DisplayName())
				roomMsgs = append(roomMsgs, guardMsg)
				if e.sendToPlayer != nil {
					e.sendToPlayer(player.FirstName, []string{guardMsg})
					e.sendToPlayer(guard.FirstName, []string{guardMsg})
				}
				// Redirect to the guard
				return e.monsterAttackPlayer(inst, def, guard)
			}
		}
	}

	// Summoned elemental guard: intercept melee attacks on the guarded player.
	// Ranged attacks bypass the guard.
	if e.monsterMgr != nil {
		isRanged := false
		for _, w := range def.Weapons {
			if itemDef := e.items[w.Archetype]; itemDef != nil {
				t := itemDef.Type
				if t == "BOW_WEAPON" || t == "HANDGUN" || t == "RIFLE" || t == "THROWN_WEAPON" {
					isRanged = true
					break
				}
			}
		}
		if !isRanged {
			if intercepted, iMsgs, summonerStun := e.elementalGuardIntercept(inst, def, player); intercepted {
				roomMsgs = append(roomMsgs, iMsgs...)
				if summonerStun != "" && e.sessions != nil {
					for _, p := range e.sessions.OnlinePlayers() {
						if p.FirstName == summonerStun {
							stunSecs := 2 + rand.Intn(4)
							p.RoundTimeExpiry = time.Now().Add(time.Duration(stunSecs) * time.Second)
							// A real stun, not just round time — ADVANCE/RETREAT/FLEE
							// gate on Stunned, not RoundTimeExpiry.
							p.Stunned = true
							p.StunExpiry = p.RoundTimeExpiry
							p.SummonedCreatureID = 0
							e.setWatching(summonerStun, 0)
							if e.sendToPlayer != nil {
								e.sendToPlayer(summonerStun, []string{"The loss of your summoned creature sends a wave of psychic shock through you!"})
							}
							break
						}
					}
				}
				return playerMsgs, roomMsgs, nil
			}
		}
	}

	name := FormatMonsterName(def, e.monAdjs)
	article := articleFor(name, def.Unique)
	capArt := capArticle(article)

	// Special attacks and spell casts are handled a level up, in monsterCombatTick
	// (via monsterTrySpecialAttack/monsterTryStartCast) — they're ranged "attack forms"
	// per GMSCRIPT.DOC that can target any current attacker, not just this monster's
	// single locked melee target, so they're resolved before monsterAttackPlayer is
	// ever called for a normal-attack tick.

	// Normal attack
	monWeaponName := e.monsterWeaponName(def)
	monVerb, monDmgNoun := monsterAttackVerb(def, e.items)

	playerMsgs = append(playerMsgs, fmt.Sprintf("%s%s %s %s with its %s.", capArt, name, monVerb, player.FirstName, monWeaponName))

	// Weather modifier for monsters too
	wMod := e.weatherMod(inst.RoomNumber)
	defRating := playerDefenseRating(player) + armorEnchantBonus(player, e.items) + shieldDefenseBonus(player, e.items) + e.massProtectionGroupBonus(player)
	// Multi-attacker penalty: -5 per 2 additional attackers beyond the first
	if e.monsterMgr != nil {
		attackerCount := 0
		for i := range e.monsterMgr.instances {
			mi := &e.monsterMgr.instances[i]
			if mi.Alive && mi.Target == player.FirstName && mi.RoomNumber == player.RoomNumber {
				attackerCount++
			}
		}
		if attackerCount > 1 {
			defRating -= (attackerCount - 1) * 5 / 2
		}
	}
	toHit := calcToHit(def.Attack1+wMod, defRating)
	roll := rand.Intn(100) + 1

	if roll >= toHit {
		// A stunned or knocked-down player is defenseless — any successful hit
		// against one is an automatic critical, not just a natural 96+ roll (see
		// the matching comment in doAttackMonster for the player-attacking-monster
		// direction).
		excellent := roll >= 96 || player.Stunned || player.KnockedDown
		hitLabel := "Hit!"
		if excellent {
			hitLabel = "Excellent Hit!"
		}
		// Open-ended roll: 96-100 adds a bonus roll — mirrors doAttackMonster's
		// player-vs-monster version, which players already get.
		openEndedBonus := 0
		if excellent {
			bonus := rand.Intn(100) + 1
			openEndedBonus = bonus / 2
			if bonus >= 96 {
				hitLabel = "Devastating Critical!!"
				openEndedBonus += rand.Intn(100)/2 + 50
			}
		}
		playerMsgs = append(playerMsgs, fmt.Sprintf(" [ToHit: %d, Roll: %d] %s", toHit, roll, hitLabel))

		if player.MistForm {
			playerMsgs = append(playerMsgs, fmt.Sprintf(" %s%s attack passes harmlessly through %s misty form!", capArt, name, player.Possessive()))
			roomMsgs = append(roomMsgs, fmt.Sprintf("%s%s %s %s, but the attack passes harmlessly through %s misty form!", capArt, name, monVerb, player.FirstName, player.Possessive()))
			return playerMsgs, roomMsgs, player
		}

		dmg := monsterDamage(def)
		if openEndedBonus > 0 {
			dmg = dmg * (100 + openEndedBonus) / 100
		}
		part, locMult := rollBodyPart("HUMAN", 0)
		armorPct := armorDRForBodyPart(player, e.items, part)
		dmg = applyArmor(dmg, armorPct)
		if dmg <= 0 {
			dmg = 1
		}

		dmg = dmg * locMult / 100
		if dmg <= 0 {
			dmg = 1
		}
		dmg = formDamageReduction(player, dmg) // Slime Form: 90% reduction; everyone else unchanged
		var monWeaponDef *gameworld.ItemDef
		if len(def.Weapons) > 0 {
			monWeaponDef = e.items[def.Weapons[0].Archetype]
		}
		dtype := damageTypeForWeapon(monWeaponDef, e)
		woundLevel := woundLevelFromDamage(dmg, player.MaxBodyPoints)
		player.Wounds = applyWoundToList(player.Wounds, part, dtype, woundLevel, !player.Undead)
		player.Bleeding = anyBleeding(player.Wounds)
		player.BodyPoints -= dmg
		rawBP := player.BodyPoints
		if player.BodyPoints < 0 {
			player.BodyPoints = 0
		}

		playerMsgs = append(playerMsgs, fmt.Sprintf(" %s %s to %s. [%d Damage]", damageSeverity(dmg, player.MaxBodyPoints), monDmgNoun, part, dmg))
		if interruptMsg := interruptPreparedSpell(player); interruptMsg != "" {
			playerMsgs = append(playerMsgs, interruptMsg)
		}

		// Monster poison/disease/fatigue on hit. GMSCRIPT.DOC immunity #7 ("life
		// affecting") exempts undead from poison/disease due to lacking an active
		// life force — mirrors the !player.Undead bleeding exemption in wounds.go.
		if !player.Undead {
			if def.PoisonChance > 0 && rand.Intn(100) < def.PoisonChance {
				player.Poisoned = true
				if 1 > player.PoisonLevel {
					player.PoisonLevel = 1
				}
				playerMsgs = append(playerMsgs, " You feel poison coursing through your veins!")
			}
			if def.DiseaseChance > 0 && rand.Intn(100) < def.DiseaseChance {
				player.Diseased = true
				if 1 > player.DiseaseLevel {
					player.DiseaseLevel = 1
				}
				playerMsgs = append(playerMsgs, " You feel a sickness taking hold!")
			}
		}
		if def.FatigueChance > 0 && rand.Intn(100) < def.FatigueChance {
			drain := def.FatigueLevel
			if drain <= 0 {
				drain = 5
			}
			player.Fatigue -= drain
			if player.Fatigue < 0 {
				player.Fatigue = 0
			}
			playerMsgs = append(playerMsgs, " You feel your life force being drained!")
		}

		// Stun/knockdown stagger on an excellent hit — mirrors doAttackMonster's
		// player-vs-monster version (30% chance, not on a killing/incapacitating
		// blow). Knockdown is skipped while flying (Position 4): see the "Flying
		// Position stranding bug" memory — silently grounding a flying player via
		// combat would strand them with no floor, so they're staggered by stun
		// instead, same as a monster with no legs to knock out from under it.
		wasStunned := false
		staggerBroadcast := ""
		if excellent && rawBP > 0 {
			if rand.Intn(100) < 30 {
				wasStunned = true
				// Only a standing player can be knocked off their feet — not one
				// already laying/sitting/kneeling (incl. from an earlier knockdown,
				// until they STAND), nor a flying one.
				canKnockDown := player.Position == 0
				if rand.Intn(100) < 50 || !canKnockDown {
					stunSecs := 3 + rand.Intn(4)
					player.Stunned = true
					player.StunExpiry = time.Now().Add(time.Duration(stunSecs) * time.Second)
					playerMsgs = append(playerMsgs, " You are stunned!")
					staggerBroadcast = ", stun"
				} else {
					player.KnockedDown = true
					player.Position = 2
					playerMsgs = append(playerMsgs, " You are knocked off your feet!")
					staggerBroadcast = ", knockdown"
				}
			}
		}

		// Build simplified 3rd-person broadcast for monster attack
		monBroadcast := fmt.Sprintf("%s%s %s %s. %s %s", capArt, name, monVerb, player.FirstName, hitLabel, simplifiedDamageTier(dmg))
		if wasStunned {
			monBroadcast += staggerBroadcast
		}
		monBroadcast += "."
		if rawBP <= 0 {
			// Arena prevents full death (and unconsciousness)
			if e.isArenaRoom(player.RoomNumber) {
				player.BodyPoints = 1
				playerMsgs = append(playerMsgs, " The arena's enchantment prevents your death!")
			} else {
				outcomeMsgs, died := e.resolveDirectHitOutcome(player, rawBP, name)
				if died {
					playerMsgs = append(playerMsgs, fmt.Sprintf(" %s%s slays %s.", capArt, name, player.FirstName))
					playerMsgs = append(playerMsgs, outcomeMsgs...)
					monBroadcast += fmt.Sprintf(" %s%s slays %s!", capArt, name, player.FirstName)
				} else {
					playerMsgs = append(playerMsgs, outcomeMsgs...)
					monBroadcast += fmt.Sprintf(" %s%s knocks %s unconscious!", capArt, name, player.FirstName)
				}
			}
		}
		roomMsgs = append(roomMsgs, monBroadcast)
	} else {
		playerMsgs = append(playerMsgs, fmt.Sprintf(" [ToHit: %d, Roll: %d] Miss.", toHit, roll))
		roomMsgs = append(roomMsgs, fmt.Sprintf("%s%s %s %s. Miss.", capArt, name, monVerb, player.DisplayName()))
	}

	return playerMsgs, roomMsgs, player
}

// ---- Monster special attacks and spellcasting ----
//
// Both are ranged "attack forms" per GMSCRIPT.DOC ("special attack forms" for SPECUSE,
// and spells are cast as a gesture rather than a melee swing) so — unlike the monster's
// normal melee attack, which always targets whoever it's locked onto (inst.Target) —
// they pick randomly among whoever is currently attacking it, or failing that, anyone
// in the room. They also bypass guard interception entirely (see monsterAttackPlayer's
// guard-redirect/elemental-guard-intercept, which only applies to the normal attack).

// monsterAttackTargetPool returns the candidate targets for a monster's special attack
// or spell cast: players currently fighting it (CombatTarget pointed at this instance),
// or every valid player in its room if nobody currently is.
func (e *GameEngine) monsterAttackTargetPool(inst *MonsterInstance) []*Player {
	if e.sessions == nil {
		return nil
	}
	var attackers, roomPlayers []*Player
	for _, p := range e.sessions.OnlinePlayers() {
		if p.RoomNumber != inst.RoomNumber || p.Dead || p.IsConcealed() {
			continue
		}
		roomPlayers = append(roomPlayers, p)
		if p.CombatTarget != nil && p.CombatTarget.IsMonster && p.CombatTarget.MonsterID == inst.ID {
			attackers = append(attackers, p)
		}
	}
	if len(attackers) > 0 {
		return attackers
	}
	return roomPlayers
}

// broadcastCombatRoom sends a monster combat room-broadcast, excluding defender (who
// already got their own private detail message via sendToPlayer and would otherwise
// see the same hit described twice — once in detail, once in the room's simplified
// recap). Falls back to a plain broadcast if defender is nil or no exclude func is
// wired up (e.g. in tests).
func (e *GameEngine) broadcastCombatRoom(roomNum int, defender *Player, messages []string) {
	if len(messages) == 0 {
		return
	}
	if defender != nil && e.localRoomBroadcastExclude != nil {
		e.localRoomBroadcastExclude(roomNum, defender.FirstName, messages)
		return
	}
	if e.localRoomBroadcast != nil {
		e.localRoomBroadcast(roomNum, messages)
	}
}

// monsterDodgeChance is the Combat Maneuvering skill's chance to completely avoid a
// monster's special attack or spell — 2% per rank, capped at 95%, per LEGENDS.DOC.
func monsterDodgeChance(target *Player) int {
	chance := target.Skills[10] * 2
	if chance > 95 {
		chance = 95
	}
	return chance
}

// applyMonsterElementalDamageToPlayer applies a monster's ranged hit (special attack or
// spell) to player: armor%, Drakin heat/cold vulnerability, Endurance skill reduction,
// Heat/Cold Shield mitigation, then rolls a body part and applies the wound. Returns the
// final damage dealt, the body part hit, the wound damage-type noun (for dmgNounForType),
// and the player's raw (pre-clamp) BodyPoints after the hit, for resolveDirectHitOutcome.
// applyLocationMult controls whether the rolled body part's hit-location multiplier
// (100% vitals / 40% limb / 20% extremity) scales the damage down: true for special
// attacks (the original ported behavior), false for spells — castDamageSpell never
// reduces a player-cast spell's damage by the monster's body part, only uses it for
// flavor text, so a monster-cast spell shouldn't reduce the player's damage by it either
// ("Spell attacks should work like any normal spell casting").
func (e *GameEngine) applyMonsterElementalDamageToPlayer(player *Player, dmg int, dmgType string, applyLocationMult bool) (finalDmg int, part string, dtype string, rawBP int) {
	var locMult int
	part, locMult = rollBodyPart("HUMAN", 0)
	armorPct := armorDRForBodyPart(player, e.items, part)
	dmg = applyArmor(dmg, armorPct)
	dmg = applyDrakinElementalVulnerability(player, dmgType, dmg)

	upperType := strings.ToUpper(dmgType)
	if enduranceSkill := player.Skills[11]; enduranceSkill > 0 && (upperType == "HEAT" || upperType == "COLD" || upperType == "ELECTRIC") {
		reduction := enduranceSkill
		if reduction > 50 {
			reduction = 50
		}
		dmg = dmg * (100 - reduction) / 100
	}

	now := time.Now()
	if upperType == "HEAT" && !player.HeatShieldExpiry.IsZero() && now.Before(player.HeatShieldExpiry) {
		dmg /= 2
	}
	if upperType == "COLD" && !player.ColdShieldExpiry.IsZero() && now.Before(player.ColdShieldExpiry) {
		dmg /= 2
	}

	if applyLocationMult {
		dmg = dmg * locMult / 100
	}
	if dmg <= 0 {
		dmg = 1
	}
	dmg = formDamageReduction(player, dmg) // Mist Form: immune; Slime Form: 90% reduction
	dtype = damageTypeForSpecAttack(dmgType)
	level := woundLevelFromDamage(dmg, player.MaxBodyPoints)
	player.Wounds = applyWoundToList(player.Wounds, part, dtype, level, !player.Undead)
	player.Bleeding = anyBleeding(player.Wounds)
	player.BodyPoints -= dmg
	rawBP = player.BodyPoints
	if player.BodyPoints < 0 {
		player.BodyPoints = 0
	}
	return dmg, part, dtype, rawBP
}

// monsterTrySpecialAttack rolls a monster's SPECUSE chance (skipping entirely once
// SPECUSES uses have been spent) and, on success, resolves a special attack against a
// randomly chosen target from monsterAttackTargetPool. Returns used=false if the monster
// should try something else (spell, then normal attack) this tick instead.
func (e *GameEngine) monsterTrySpecialAttack(inst *MonsterInstance, def *gameworld.MonsterDef) (playerMsgs, roomMsgs []string, defender *Player, used bool) {
	if def.SpecUse <= 0 {
		return nil, nil, nil, false
	}
	if def.SpecUses > 0 && inst.SpecAttacksUsed >= def.SpecUses {
		return nil, nil, nil, false
	}
	if rand.Intn(100) >= def.SpecUse {
		return nil, nil, nil, false
	}
	targets := e.monsterAttackTargetPool(inst)
	if len(targets) == 0 {
		return nil, nil, nil, false
	}
	target := targets[rand.Intn(len(targets))]

	e.monsterMgr.mu.Lock()
	inst.SpecAttacksUsed++
	e.monsterMgr.mu.Unlock()

	name := FormatMonsterName(def, e.monAdjs)
	article := articleFor(name, def.Unique)
	capArt := capArticle(article)

	if dodge := monsterDodgeChance(target); dodge > 0 && rand.Intn(100) < dodge {
		playerMsgs = append(playerMsgs, fmt.Sprintf("%s%s uses a special attack, but you dodge it!", capArt, name))
		return playerMsgs, nil, target, true
	}

	specText := def.TextOverrides["TEXX"]
	if specText != "" {
		specText = strings.Replace(specText, "%s", capArt+name, 1)
		specText = strings.Replace(specText, "%s", target.FirstName, 1)
	} else {
		specText = fmt.Sprintf("%s%s uses a special attack on %s!", capArt, name, target.FirstName)
	}

	dmg := def.SpecBase + rand.Intn(max(1, def.SpecDmg))
	finalDmg, part, dtype, rawBP := e.applyMonsterElementalDamageToPlayer(target, dmg, def.SpecDmgType, true)

	// The target is excluded from the room broadcast (broadcastCombatRoom) since they
	// get their own private detail instead, so specText must also go into playerMsgs
	// (undecorated) or they'd never see their own attack's announcement at all. The
	// room's copy gets a simplified damage tier too, so an observer sees whether the
	// special attack actually did anything (the exact [N Damage] stays private).
	monBroadcast := fmt.Sprintf("%s %s.", specText, simplifiedDamageTier(finalDmg))
	playerMsgs = append(playerMsgs, specText, fmt.Sprintf(" %s %s to %s. [%d Damage]", damageSeverity(finalDmg, target.MaxBodyPoints), dmgNounForType(dtype), part, finalDmg))
	if interruptMsg := interruptPreparedSpell(target); interruptMsg != "" {
		playerMsgs = append(playerMsgs, interruptMsg)
	}

	if rawBP <= 0 {
		outcomeMsgs, died := e.resolveDirectHitOutcome(target, rawBP, name)
		if died {
			playerMsgs = append(playerMsgs, fmt.Sprintf(" %s%s slays %s.", capArt, name, target.FirstName))
			playerMsgs = append(playerMsgs, outcomeMsgs...)
			monBroadcast += fmt.Sprintf(" %s%s slays %s!", capArt, name, target.FirstName)
		} else {
			playerMsgs = append(playerMsgs, outcomeMsgs...)
			monBroadcast += fmt.Sprintf(" %s%s knocks %s unconscious!", capArt, name, target.FirstName)
		}
	}
	roomMsgs = append(roomMsgs, monBroadcast)
	return playerMsgs, roomMsgs, target, true
}

// monsterTryStartCast rolls a monster's SPELLUSE chance and, if it triggers, begins a
// multi-tick spell cast: TEXS ("prepares a spell," per GMSCRIPT.DOC) shows now, and the
// spell resolves after CASTLEVEL seconds via resolveMonsterCast — unless disrupted by
// taking damage in the meantime (see damageMonster), which matches "A muldragun's spell
// is disrupted!" in original/log.txt. Mana is spent up front and never recovers; once a
// monster can't afford any of its spells it stops trying for the rest of the fight.
// Returns true if a cast was started (consuming this attack turn).
func (e *GameEngine) monsterTryStartCast(inst *MonsterInstance, def *gameworld.MonsterDef) bool {
	if inst.Casting || len(def.Spells) == 0 || def.SpellUse <= 0 {
		return false
	}
	if rand.Intn(100) >= def.SpellUse {
		return false
	}
	var affordable []int
	for _, id := range def.Spells {
		if sp := FindSpellByID(id); sp != nil && sp.ManaCost <= inst.CurrentMana {
			affordable = append(affordable, id)
		}
	}
	if len(affordable) == 0 {
		return false
	}
	targets := e.monsterAttackTargetPool(inst)
	if len(targets) == 0 {
		return false
	}
	target := targets[rand.Intn(len(targets))]
	spellID := affordable[rand.Intn(len(affordable))]
	sp := FindSpellByID(spellID)

	// Windup uses the spell's own CastTime (matching player PREPARE/CAST timing —
	// "Spell attacks should work like any normal spell casting"), not CASTLEVEL: despite
	// GMSCRIPT.DOC's "defines the duration of a creature's spell," testing showed a
	// black muldragun's Lightning Bolt (CastTime 3) resolving in ~3 seconds, not
	// CASTLEVEL's 20 — CASTLEVEL appears to govern something else (likely a buff/DoT
	// spell's effect duration once cast, not yet implemented) rather than windup time.
	castSeconds := sp.CastTime
	if castSeconds <= 0 {
		castSeconds = 3
	}

	e.monsterMgr.mu.Lock()
	inst.CurrentMana -= sp.ManaCost
	inst.Casting = true
	inst.CastSpellID = spellID
	inst.CastTarget = target.FirstName
	inst.CastExpiry = time.Now().Add(time.Duration(castSeconds) * time.Second)
	e.monsterMgr.mu.Unlock()

	if texs := def.TextOverrides["TEXS"]; texs != "" && e.localRoomBroadcast != nil {
		e.localRoomBroadcast(inst.RoomNumber, []string{texs})
	}
	return true
}

// monsterSpellHitFlavor returns the "<subject> <verb> ... at <object>!" line for a
// damage spell — the third-person text mirrors what castDamageSpell shows onlookers
// when a PLAYER casts the same spell (see the per-damage-type and per-spell-ID switch
// there), reused here so a monster casting the identical spell at a player reads the
// same way, e.g. "A black muldragun hurls a bolt of lightning at Moordread!" for spell
// 103 (matches original/icyranbro.txt). subject should already include its article
// (e.g. "A black muldragun"); object is just the target's name.
func monsterSpellHitFlavor(spell *SpellDef, subject, object string) string {
	flavor := fmt.Sprintf("%s forms a bolt of energy and hurls it at %s!", subject, object)
	switch spell.DmgType {
	case "heat":
		flavor = fmt.Sprintf("%s forms a ball of flame and hurls it at %s!", subject, object)
	case "cold":
		flavor = fmt.Sprintf("%s forms a freezing sphere from the air and hurls it at %s!", subject, object)
	case "electric":
		flavor = fmt.Sprintf("%s releases a bolt of lightning at %s!", subject, object)
	case "crushing":
		flavor = fmt.Sprintf("%s hurls a force blast at %s!", subject, object)
	}
	switch spell.ID {
	case 103: // Lightning Bolt
		flavor = fmt.Sprintf("%s hurls a bolt of lightning at %s!", subject, object)
	case 120: // Frost Ray
		flavor = fmt.Sprintf("%s points a finger at %s and a ray of intense cold shoots forth!", subject, object)
	case 345: // Spectral Sword
		flavor = fmt.Sprintf("A ghostly sword materializes before %s and slashes at %s!", subject, object)
	case 523: // Earth Spike
		flavor = fmt.Sprintf("As %s beckons to the ground, a horrible spike thrusts up from the earth and impales %s!", subject, object)
	case 354: // Rorin's Fire
		flavor = fmt.Sprintf("A wave of red and orange flames erupts from %s's hand and encircles %s, hissing and constricting like a snake!", subject, object)
	}
	return flavor
}

// monsterPsiHitFlavor is monsterSpellHitFlavor's counterpart for a monster's offensive
// psi discipline (Kinetic Thrust, Pyrokinetics, etc. — see resolveMonsterPsi). The
// Mind over Mind attacks (Psychic Blast/Crush/Terror/Pain) have no DmgType, since
// they're pure mental damage rather than an elemental one, so they get their own
// per-ID lines instead of falling through the DmgType switch.
func monsterPsiHitFlavor(disc *PsiDiscipline, subject, object string) string {
	flavor := fmt.Sprintf("%s focuses psychic energy at %s!", subject, object)
	switch disc.DmgType {
	case "heat":
		flavor = fmt.Sprintf("%s hurls a mote of searing psychic fire at %s!", subject, object)
	case "cold":
		flavor = fmt.Sprintf("%s chills the air around %s with a wave of psychic cold!", subject, object)
	case "electric":
		flavor = fmt.Sprintf("%s crackles with psychic energy that arcs toward %s!", subject, object)
	case "crushing":
		flavor = fmt.Sprintf("%s unleashes a wave of kinetic force at %s!", subject, object)
	}
	switch disc.ID {
	case 53: // Psychic Blast
		flavor = fmt.Sprintf("%s unleashes a psychic blast at %s!", subject, object)
	case 55: // Psychic Crush
		flavor = fmt.Sprintf("%s's mind clenches like a fist around %s's thoughts!", subject, object)
	case 56: // Terror
		flavor = fmt.Sprintf("%s floods %s's mind with images of unspeakable horror!", subject, object)
	case 65: // Pain
		flavor = fmt.Sprintf("%s reaches into %s's mind and wrings it with agony!", subject, object)
	}
	return flavor
}

// resolveMonsterCast finishes a spell cast begun by monsterTryStartCast once the
// windup (the spell's own CastTime) has passed: rolls the monster's spellcraft check
// (SPELLSKILL) and, on success, shows TEXL (the "gestures/casts" line, per
// GMSCRIPT.DOC) aimed at the stored target, then the spell's own hit-flavor line
// (monsterSpellHitFlavor) and damage resolution — reusing the same dice, damage type,
// and player-side mitigations (armor/Drakin/Endurance/shields) as a player casting the
// same spell. On failure shows TEXQ ("fails its spellcraft check").
func (e *GameEngine) resolveMonsterCast(inst *MonsterInstance, def *gameworld.MonsterDef) (playerMsgs, roomMsgs []string, defender *Player) {
	e.monsterMgr.mu.Lock()
	spellID := inst.CastSpellID
	targetName := inst.CastTarget
	inst.Casting = false
	inst.CastSpellID = 0
	inst.CastTarget = ""
	inst.CastExpiry = time.Time{}
	e.monsterMgr.mu.Unlock()

	sp := FindSpellByID(spellID)
	if sp == nil || e.sessions == nil {
		return nil, nil, nil
	}
	var target *Player
	for _, p := range e.sessions.OnlinePlayers() {
		if p.FirstName == targetName && p.RoomNumber == inst.RoomNumber && !p.Dead {
			target = p
			break
		}
	}
	if target == nil || target.IsConcealed() {
		return nil, nil, nil
	}

	name := FormatMonsterName(def, e.monAdjs)
	article := articleFor(name, def.Unique)
	capArt := capArticle(article)

	texl := def.TextOverrides["TEXL"]
	gestureMsg := fmt.Sprintf("%s%s gestures and casts a spell at %s.", capArt, name, target.FirstName)
	if texl != "" {
		gestureMsg = fmt.Sprintf("%s at %s.", texl, target.FirstName)
	}
	roomMsgs = append(roomMsgs, gestureMsg)

	// Spellcraft check (SPELLSKILL, "base spellcraft skill" per GMSCRIPT.DOC): mirrors
	// the shape of the player formula (25 base + skill-derived bonus, capped 95%), with
	// SPELLSKILL treated as an already-scaled bonus — a judgment call, since the doc
	// doesn't give the exact formula, but it matches original/log.txt: every recorded
	// black muldragun cast either lands or gets disrupted, never shows its TEXQ miss.
	castChance := 25 + def.SpellSkill
	if castChance > 95 {
		castChance = 95
	}
	if castChance < 5 {
		castChance = 5
	}

	if rand.Intn(100) >= castChance {
		texq := def.TextOverrides["TEXQ"]
		if texq == "" {
			texq = fmt.Sprintf("%s%s's spell fizzles.", capArt, name)
		}
		roomMsgs = append(roomMsgs, texq)
		return nil, roomMsgs, nil
	}

	// hitFlavor is the actual "X hurls a bolt of lightning at Y!" line — mirrors the
	// third-person text castDamageSpell shows onlookers when a player casts the same
	// spell (see monsterSpellHitFlavor), matching e.g. original/icyranbro.txt's "A
	// black muldragun hurls a bolt of lightning at Vaulle!" for spell 103. It's a
	// separate line from the TEXL gesture above — that only announces the windup.
	//
	// The target is excluded from the room broadcast (broadcastCombatRoom) once we
	// know they're the defender, so gestureMsg/hitFlavor must also go into playerMsgs
	// (undecorated — no damage tier, since the target gets the exact number instead)
	// or the target would never see their own spell's announcement at all.
	hitFlavor := monsterSpellHitFlavor(sp, capArt+name, target.FirstName)

	if dodge := monsterDodgeChance(target); dodge > 0 && rand.Intn(100) < dodge {
		roomMsgs = append(roomMsgs, fmt.Sprintf("%s %s dodges out of the way!", hitFlavor, target.DisplayName()))
		playerMsgs = append(playerMsgs, gestureMsg, hitFlavor, "You dodge out of the way!")
		return playerMsgs, roomMsgs, target
	}

	// Spell Shield (234, "+25 magic resistance" per MAGIC.TXT) gives the target a
	// flat chance to deflect the spell outright, on top of the monster's own
	// cast-chance and the target's dodge above.
	if resist := spellShieldResistBonus(target); resist > 0 && rand.Intn(100) < resist {
		roomMsgs = append(roomMsgs, fmt.Sprintf("%s but an antimagical field around %s deflects it!", hitFlavor, target.DisplayName()))
		playerMsgs = append(playerMsgs, gestureMsg, hitFlavor, "Your antimagical field flares and deflects the spell!")
		return playerMsgs, roomMsgs, target
	}

	dmg := rand.Intn(sp.DmgMax-sp.DmgMin+1) + sp.DmgMin
	if dmg <= 0 {
		dmg = 1
	}
	finalDmg, part, dtype, rawBP := e.applyMonsterElementalDamageToPlayer(target, dmg, sp.DmgType, false)

	// Public room line always gets a simplified damage tier — without this an observer
	// only sees the spell fired, never whether it did anything (the exact [N Damage]
	// figure stays private, matching how normal/special attacks report to the room).
	monBroadcast := fmt.Sprintf("%s %s.", hitFlavor, simplifiedDamageTier(finalDmg))
	playerMsgs = append(playerMsgs, gestureMsg, hitFlavor, fmt.Sprintf(" %s %s to %s. [%d Damage]", damageSeverity(finalDmg, target.MaxBodyPoints), dmgNounForType(dtype), part, finalDmg))
	if interruptMsg := interruptPreparedSpell(target); interruptMsg != "" {
		playerMsgs = append(playerMsgs, interruptMsg)
	}

	if rawBP <= 0 {
		outcomeMsgs, died := e.resolveDirectHitOutcome(target, rawBP, name)
		if died {
			playerMsgs = append(playerMsgs, fmt.Sprintf(" %s%s slays %s.", capArt, name, target.FirstName))
			playerMsgs = append(playerMsgs, outcomeMsgs...)
			monBroadcast += fmt.Sprintf(" %s%s slays %s!", capArt, name, target.FirstName)
		} else {
			playerMsgs = append(playerMsgs, outcomeMsgs...)
			monBroadcast += fmt.Sprintf(" %s%s knocks %s unconscious!", capArt, name, target.FirstName)
		}
	}
	roomMsgs = append(roomMsgs, monBroadcast)
	return playerMsgs, roomMsgs, target
}

// monsterTryStartPsi rolls a monster's PSIUSE chance and, if it triggers, begins a
// multi-tick psionic attack — mirrors monsterTryStartCast, but only "damage"-effect
// disciplines from def.Disciplines are eligible (defense/buff disciplines are handled
// as always-active passives, see monsterPsiDefenseBonus; utility ones like Telepathy
// or Teleportation make no sense as an "attack"). Psi points are spent up front and
// never recover, same as CurrentMana. Unlike spellcasting, this isn't gated by
// inst.Silenced — psionics are a mental discipline, not an incantation. Returns true
// if an attack was started (consuming this attack turn).
func (e *GameEngine) monsterTryStartPsi(inst *MonsterInstance, def *gameworld.MonsterDef) bool {
	if inst.PsiCasting || len(def.Disciplines) == 0 || def.PsiUse <= 0 {
		return false
	}
	if rand.Intn(100) >= def.PsiUse {
		return false
	}
	var affordable []int
	for _, id := range def.Disciplines {
		if disc := FindPsiByID(id); disc != nil && disc.Effect == "damage" && disc.PsiCost <= inst.CurrentPsi {
			affordable = append(affordable, id)
		}
	}
	if len(affordable) == 0 {
		return false
	}
	targets := e.monsterAttackTargetPool(inst)
	if len(targets) == 0 {
		return false
	}
	target := targets[rand.Intn(len(targets))]
	discID := affordable[rand.Intn(len(affordable))]
	disc := FindPsiByID(discID)

	windupSeconds := disc.RoundSec
	if windupSeconds <= 0 {
		windupSeconds = 5
	}

	e.monsterMgr.mu.Lock()
	inst.CurrentPsi -= disc.PsiCost
	inst.PsiCasting = true
	inst.PsiCastDiscID = discID
	inst.PsiCastTarget = target.FirstName
	inst.PsiCastExpiry = time.Now().Add(time.Duration(windupSeconds) * time.Second)
	e.monsterMgr.mu.Unlock()

	name := FormatMonsterName(def, e.monAdjs)
	article := articleFor(name, def.Unique)
	if e.localRoomBroadcast != nil {
		e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("%s%s stares intently at %s, brow furrowed in concentration.", capArticle(article), name, target.FirstName)})
	}
	return true
}

// resolveMonsterPsi finishes a psionic attack begun by monsterTryStartPsi once the
// windup has passed — mirrors resolveMonsterCast, but the success check is
// playerPsiResistRoll (psionics.go) rather than a monster-side spellcraft roll: per
// GMSCRIPT.DOC, "PSISKILL... the total chance that the psionic creature has when
// invoking a psionic power... Powerful psionic creatures should have well over 100 so
// as to defeat a player's natural resistance to psionics" — so it's the target's
// resistance (Willpower-based baseline, plus Cloak Mind's +25, see
// cloakMindResistBonus) being weighed against the monster's PSISKILL, not a bare
// fizzle chance independent of the target.
func (e *GameEngine) resolveMonsterPsi(inst *MonsterInstance, def *gameworld.MonsterDef) (playerMsgs, roomMsgs []string, defender *Player) {
	e.monsterMgr.mu.Lock()
	discID := inst.PsiCastDiscID
	targetName := inst.PsiCastTarget
	inst.PsiCasting = false
	inst.PsiCastDiscID = 0
	inst.PsiCastTarget = ""
	inst.PsiCastExpiry = time.Time{}
	e.monsterMgr.mu.Unlock()

	disc := FindPsiByID(discID)
	if disc == nil || e.sessions == nil {
		return nil, nil, nil
	}
	var target *Player
	for _, p := range e.sessions.OnlinePlayers() {
		if p.FirstName == targetName && p.RoomNumber == inst.RoomNumber && !p.Dead {
			target = p
			break
		}
	}
	if target == nil || target.IsConcealed() {
		return nil, nil, nil
	}

	name := FormatMonsterName(def, e.monAdjs)
	article := articleFor(name, def.Unique)
	capArt := capArticle(article)

	gestureMsg := fmt.Sprintf("%s%s's eyes flash as they focus their will upon %s.", capArt, name, target.FirstName)
	roomMsgs = append(roomMsgs, gestureMsg)

	hitFlavor := monsterPsiHitFlavor(disc, capArt+name, target.FirstName)

	if dodge := monsterDodgeChance(target); dodge > 0 && rand.Intn(100) < dodge {
		roomMsgs = append(roomMsgs, fmt.Sprintf("%s %s dodges out of the way!", hitFlavor, target.DisplayName()))
		playerMsgs = append(playerMsgs, gestureMsg, hitFlavor, "You dodge out of the way!")
		return playerMsgs, roomMsgs, target
	}

	if playerPsiResistRoll(target, def.PsiSkill) {
		roomMsgs = append(roomMsgs, fmt.Sprintf("%s %s shrugs it off, unaffected!", hitFlavor, target.DisplayName()))
		playerMsgs = append(playerMsgs, gestureMsg, hitFlavor, "You shrug off the psychic assault!")
		return playerMsgs, roomMsgs, target
	}

	dmg := rand.Intn(disc.DmgMax-disc.DmgMin+1) + disc.DmgMin
	if dmg <= 0 {
		dmg = 1
	}
	finalDmg, part, dtype, rawBP := e.applyMonsterElementalDamageToPlayer(target, dmg, disc.DmgType, false)

	monBroadcast := fmt.Sprintf("%s %s.", hitFlavor, simplifiedDamageTier(finalDmg))
	playerMsgs = append(playerMsgs, gestureMsg, hitFlavor, fmt.Sprintf(" %s %s to %s. [%d Damage]", damageSeverity(finalDmg, target.MaxBodyPoints), dmgNounForType(dtype), part, finalDmg))
	if interruptMsg := interruptPreparedSpell(target); interruptMsg != "" {
		playerMsgs = append(playerMsgs, interruptMsg)
	}

	if rawBP <= 0 {
		outcomeMsgs, died := e.resolveDirectHitOutcome(target, rawBP, name)
		if died {
			playerMsgs = append(playerMsgs, fmt.Sprintf(" %s%s slays %s.", capArt, name, target.FirstName))
			playerMsgs = append(playerMsgs, outcomeMsgs...)
			monBroadcast += fmt.Sprintf(" %s%s slays %s!", capArt, name, target.FirstName)
		} else {
			playerMsgs = append(playerMsgs, outcomeMsgs...)
			monBroadcast += fmt.Sprintf(" %s%s knocks %s unconscious!", capArt, name, target.FirstName)
		}
	}
	roomMsgs = append(roomMsgs, monBroadcast)
	return playerMsgs, roomMsgs, target
}

// ---- Death ----

// resolveDirectHitOutcome decides what happens when a single direct hit — a weapon
// attack or a spell, as opposed to an ongoing bleed/poison/disease tick — brings a
// player to or below 0 body points. rawBP is the value from BEFORE it gets clamped to
// 0 for storage; that distinction is exactly the rule: landing at precisely 0 knocks
// the player out (same presentation as being put to sleep by a Slumber spell — laying
// down, unconscious, until healed or naturally regenerated back above 0), while
// anything that drives them below 0 is lethal. Callers must clamp player.BodyPoints to
// 0 themselves before/around this call; this only sets Unconscious+Position or defers
// to handlePlayerDeath. Returns messages to append for the player, and whether they died.
func (e *GameEngine) resolveDirectHitOutcome(player *Player, rawBP int, killerName string) (msgs []string, died bool) {
	if rawBP == 0 {
		player.Unconscious = true
		player.Position = 2
		return []string{fmt.Sprintf(" %s collapses, unconscious.", player.FirstName)}, false
	}
	return e.handlePlayerDeath(player, killerName), true
}

// wakeFromUnconscious clears Unconscious and restores a standing position the moment a
// player's body points rise back above 0 — called anywhere body points are increased
// (healing spells, TEND, natural regen) so recovery is immediate rather than waiting for
// the next regen tick. Returns a message to show the player if they woke up, or "" if
// nothing changed.
func wakeFromUnconscious(p *Player) string {
	if p.Unconscious && p.BodyPoints > 0 {
		p.Unconscious = false
		p.Position = 0
		return "You regain consciousness!"
	}
	return ""
}

func (e *GameEngine) handlePlayerDeath(player *Player, killerName string) []string {
	player.Unconscious = false
	player.Dead = true
	player.CombatTarget = nil
	player.Joined = false
	player.Position = 2 // laying down
	// A corpse can't stay hidden/invisible/phantom-formed — it needs to be
	// visible so other players can find and raise it.
	player.Hidden = false
	player.Invisible = false
	player.PhantomForm = false
	e.lastDeathRoom = player.RoomNumber

	e.dismissSummonedCreature(player)
	e.clearPlayerFromGuards(player.FirstName)

	// XP penalty: lose up to 90% of XP towards current build point
	rate := getXPPerBP(player.Level)
	if rate > 0 {
		xpInCurrentBP := player.Experience % rate
		penalty := xpInCurrentBP * 90 / 100
		player.Experience -= penalty
		if player.Experience < 0 {
			player.Experience = 0
		}
		recalcBuildPoints(player)
	}

	e.Events.Publish("combat", fmt.Sprintf("%s was killed by %s in room %d", player.FirstName, killerName, player.RoomNumber))

	// Death telepathy: players with psionic abilities sense the death
	if e.sessions != nil {
		deathMsg := fmt.Sprintf("Your thoughts are jarred as you sense the death of %s.", player.FirstName)
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == player.FirstName || p.Dead {
				continue
			}
			// Anyone with Psionics skill or psionic school skills
			if p.Skills[26] >= 1 || p.Skills[27] >= 1 || p.Skills[28] >= 1 || p.Skills[29] >= 1 {
				if e.sendToPlayer != nil {
					e.sendToPlayer(p.FirstName, []string{deathMsg})
				}
			}
		}
	}

	return []string{
		fmt.Sprintf(" %s collapses, unconscious.", player.FirstName),
		fmt.Sprintf(" %s slays %s.", killerName, player.FirstName),
		"",
		"You are dead and can't do much of anything beside wait for someone to attempt to raise you or for Eternity, Inc. to retrieve you. Hope you paid your premium! [You may type DEPART at any time to allow Eternity, Inc. to retrieve you.]",
	}
}

func (e *GameEngine) doDepart(player *Player) *CommandResult {
	if !player.Dead {
		return &CommandResult{Messages: []string{"You are not dead."}}
	}

	player.Dead = false
	player.Unconscious = false
	player.Position = 0
	player.Bleeding = false
	player.Stunned = false
	player.Poisoned = false
	player.PoisonLevel = 0
	player.Diseased = false
	player.DiseaseLevel = 0

	player.BodyPoints = player.MaxBodyPoints / 4
	if player.BodyPoints < 1 {
		player.BodyPoints = 1
	}

	// Send to bump/depart room (201 City Gate), not start room (3950 tutorial)
	if e.departRoom > 0 {
		player.RoomNumber = e.departRoom
	} else {
		player.RoomNumber = e.startRoom
	}

	result := e.doLook(player)
	result.Messages = append([]string{
		"Your spirit coalesces and you feel the sensation of being pulled back into the world...",
		"",
	}, result.Messages...)
	result.RoomBroadcast = []string{fmt.Sprintf("%s's spirit has returned from Eternity.", player.DisplayName())}

	return result
}

// damageMonster applies damage to a monster instance, attributed to attackerName (the
// player who dealt it — recorded as LastAttacker so a bleed-out or other no-single-blow
// death can still award kill XP; see dotKillRecipients).
// Returns (killed, woke) where woke is true if the monster was sleeping and woke up.
func (e *GameEngine) damageMonster(monsterID int, dmg int, attackerName string) (killed bool, woke bool) {
	e.monsterMgr.mu.Lock()
	defer e.monsterMgr.mu.Unlock()
	for i := range e.monsterMgr.instances {
		if e.monsterMgr.instances[i].ID == monsterID && e.monsterMgr.instances[i].Alive {
			// Imprisoned (231): immune to all damage — the primary attack/damage-spell
			// entry points (doAttackMonster, castDamageSpell) already short-circuit
			// before rolling and say so; this is the backstop for the rarer paths that
			// resolve their own target and call in here directly (psi damage, Call
			// Meteor, multi-target Chain Lightning/Flaming Arrows/Tentacles ticks).
			if e.monsterMgr.instances[i].Imprisoned {
				return false, false
			}
			if attackerName != "" {
				e.monsterMgr.instances[i].LastAttacker = attackerName
			}
			if e.monsterMgr.instances[i].Sleeping {
				e.monsterMgr.instances[i].Sleeping = false
				e.monsterMgr.instances[i].SleepExpiry = time.Time{}
				e.monsterMgr.instances[i].SleepStand = true
				woke = true
			}
			e.monsterMgr.instances[i].CurrentHP -= dmg
			if e.monsterMgr.instances[i].CurrentHP <= 0 {
				e.monsterMgr.instances[i].Alive = false
				e.monsterMgr.instances[i].CurrentHP = 0
				e.monsterMgr.instances[i].DeathTime = time.Now()
				return true, woke
			}
			// A hit while casting disrupts the spell (GMSCRIPT.DOC's NONDISRUPTABLE flag
			// exempts a monster from this) — matches "A muldragun's spell is disrupted!"
			// in original/log.txt. Mana already spent (monsterTryStartCast) is not refunded.
			if e.monsterMgr.instances[i].Casting {
				def := e.monsters[e.monsterMgr.instances[i].DefNumber]
				if def == nil || !def.NonDisruptable {
					e.monsterMgr.instances[i].Casting = false
					e.monsterMgr.instances[i].CastSpellID = 0
					e.monsterMgr.instances[i].CastTarget = ""
					e.monsterMgr.instances[i].CastExpiry = time.Time{}
					if e.localRoomBroadcast != nil && def != nil {
						name := FormatMonsterName(def, e.monAdjs)
						article := capArticle(articleFor(name, def.Unique))
						e.localRoomBroadcast(e.monsterMgr.instances[i].RoomNumber, []string{fmt.Sprintf("%s%s's spell is disrupted!", article, name)})
					}
				}
			}
			// Same disruption rule for an in-progress psi attack (see monsterTryStartPsi) —
			// GMSCRIPT.DOC doesn't document a psi-specific equivalent of NONDISRUPTABLE, so
			// this reuses the same flag/behavior as spellcasting above.
			if e.monsterMgr.instances[i].PsiCasting {
				def := e.monsters[e.monsterMgr.instances[i].DefNumber]
				if def == nil || !def.NonDisruptable {
					e.monsterMgr.instances[i].PsiCasting = false
					e.monsterMgr.instances[i].PsiCastDiscID = 0
					e.monsterMgr.instances[i].PsiCastTarget = ""
					e.monsterMgr.instances[i].PsiCastExpiry = time.Time{}
					if e.localRoomBroadcast != nil && def != nil {
						name := FormatMonsterName(def, e.monAdjs)
						article := capArticle(articleFor(name, def.Unique))
						e.localRoomBroadcast(e.monsterMgr.instances[i].RoomNumber, []string{fmt.Sprintf("%s%s's concentration is disrupted!", article, name)})
					}
				}
			}
			return false, woke
		}
	}
	return false, false
}

// addMonsterWound appends a wound to a monster instance, found by ID. Wound
// writes must go through this (rather than mutating a pointer returned by
// findMonsterInRoom/etc.) because those finders return a pointer into a
// snapshot copy, not the live instance in monsterMgr.instances.
func (e *GameEngine) addMonsterWound(monsterID int, location, damageType string, level int) {
	e.monsterMgr.mu.Lock()
	defer e.monsterMgr.mu.Unlock()
	for i := range e.monsterMgr.instances {
		if e.monsterMgr.instances[i].ID == monsterID {
			def := e.monsters[e.monsterMgr.instances[i].DefNumber]
			canBleed := def == nil || def.Race != 22 // RACE 22 = undead; no blood to lose
			e.monsterMgr.instances[i].Wounds = applyWoundToList(e.monsterMgr.instances[i].Wounds, location, damageType, level, canBleed)
			return
		}
	}
}

// removeMonsterWound removes the wound at idx from a monster instance's
// wound list, found by ID. See addMonsterWound for why this indirection is
// necessary.
func (e *GameEngine) removeMonsterWound(monsterID int, idx int) (Wound, bool) {
	e.monsterMgr.mu.Lock()
	defer e.monsterMgr.mu.Unlock()
	for i := range e.monsterMgr.instances {
		if e.monsterMgr.instances[i].ID == monsterID {
			wounds := e.monsterMgr.instances[i].Wounds
			if idx < 0 || idx >= len(wounds) {
				return Wound{}, false
			}
			removed := wounds[idx]
			e.monsterMgr.instances[i].Wounds = append(wounds[:idx], wounds[idx+1:]...)
			return removed, true
		}
	}
	return Wound{}, false
}

// ---- Monster Death ----

// groupOf returns player plus every other online member of their group (leader +
// followers), resolved to live *Player pointers. If player isn't grouped, returns just
// player. Mirrors the group-resolution logic in doSplit (social.go SPLIT command).
func (e *GameEngine) groupOf(player *Player) []*Player {
	group := []*Player{player}
	if e.sessions == nil {
		return group
	}
	var otherNames []string
	if player.IsGroupLeader {
		otherNames = player.GroupMembers
	} else if player.Following != "" {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == player.Following {
				otherNames = append([]string{p.FirstName}, p.GroupMembers...)
				break
			}
		}
	}
	for _, name := range otherNames {
		if name == player.FirstName {
			continue
		}
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == name {
				group = append(group, p)
				break
			}
		}
	}
	return group
}

// dotKillRecipients resolves who should receive XP for a damage-over-time kill (bleed-
// out, tentacle DOT, poison, or disease) that has no single decisive blow: the last
// player to damage the monster (lastAttackerName — e.g. MonsterInstance.LastAttacker or
// TentacleCasterName). Sharing with the rest of their group is gated on SHAREDXP exactly
// like a normal kill (sharedXPRecipients — same room, alive, leader has it toggled on);
// if it's off, or they aren't grouped, the last attacker alone gets full credit, same as
// a direct kill. When sharing is active, anyone hidden or invisible is additionally
// excluded — a DOT kill has no clean single participant to fall back to attributing it
// to, so concealment is used as a weak signal of "wasn't really part of this." A solo
// attacker always gets credit regardless of concealment. Returns nil if the last
// attacker isn't online, or if they're sharing and everyone eligible is concealed — in
// that case the kill is unattributed (no XP, just a broadcast), same as before this
// tracking existed.
func (e *GameEngine) dotKillRecipients(lastAttackerName string) []*Player {
	if lastAttackerName == "" || e.sessions == nil {
		return nil
	}
	var attacker *Player
	for _, p := range e.sessions.OnlinePlayers() {
		if p.FirstName == lastAttackerName {
			attacker = p
			break
		}
	}
	if attacker == nil {
		return nil
	}
	group := e.sharedXPRecipients(attacker)
	if len(group) == 1 {
		return group
	}
	var visible []*Player
	for _, p := range group {
		if !p.IsConcealed() {
			visible = append(visible, p)
		}
	}
	return visible
}

// sharedXPRecipients resolves who should receive XP for killer's kill under the
// SHAREDXP toggle (see doSharedXP, social.go): killer's whole group, if their group's
// leader has SHAREDXP active, filtered down to whoever is actually still online, alive,
// and in the same room as the kill — eligibility is re-checked live at the moment of
// death (GMSCRIPT-era "within the required group/combat area" rule) rather than trusting
// stale group membership, so a member who wandered off to a different room doesn't
// collect XP for a fight they weren't part of. Falls back to just killer — the normal
// solo-kill path — if SHAREDXP isn't active for their group, or they aren't grouped.
func (e *GameEngine) sharedXPRecipients(killer *Player) []*Player {
	solo := []*Player{killer}
	if e.sessions == nil {
		return solo
	}
	var leader *Player
	if killer.IsGroupLeader {
		leader = killer
	} else if killer.Following != "" {
		leader = e.findOnlinePlayerByName(killer.Following)
	}
	if leader == nil || !leader.SharedXP {
		return solo
	}
	var eligible []*Player
	for _, p := range e.groupOf(killer) {
		if p.RoomNumber == killer.RoomNumber && !p.Dead {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) == 0 {
		return solo
	}
	return eligible
}

// computeGroupXPShares splits baseXP across recipients for a group kill (SHAREDXP —
// see doSharedXP/sharedXPRecipients — or a DOT kill's dotKillRecipients). For a single
// recipient (the ordinary solo-kill case every other call site still uses) this is just
// baseXP unchanged. For a group:
//
//  1. The pool grows 25% per member beyond the first (2p=125%, 3p=150%, 4p=175%, ...) —
//     grouping should net more total XP than fighting alone, to make it worth forming
//     a party in the first place.
//  2. Everyone starts with an equal cut of that pool.
//  3. Each member's cut is nudged by how far their level sits from the group's average
//     — modestly (2% per level of difference) and capped at ±35%, so a much-lower-level
//     member's share is reduced, not zeroed, and a much-higher-level member's is boosted,
//     not doubled.
//  4. Those nudged shares are renormalized back to sum to exactly the pool.
//  5. Finally, an extreme level spread across the whole group (max level - min level)
//     shrinks the pool itself — normal groups are untouched, a level 50 + level 2 pairing
//     lands deep in the reduced range — to blunt power-leveling through a huge level gap
//     without needing per-member eligibility cutoffs.
func computeGroupXPShares(baseXP int, recipients []*Player) []int {
	n := len(recipients)
	switch {
	case n == 0:
		return nil
	case n == 1:
		return []int{baseXP}
	}

	pool := float64(baseXP) * (1.0 + 0.25*float64(n-1))
	equalShare := pool / float64(n)

	totalLevel := 0
	minLevel, maxLevel := recipients[0].Level, recipients[0].Level
	for _, p := range recipients {
		totalLevel += p.Level
		if p.Level < minLevel {
			minLevel = p.Level
		}
		if p.Level > maxLevel {
			maxLevel = p.Level
		}
	}
	avgLevel := float64(totalLevel) / float64(n)

	const perLevelWeight = 0.02  // each level of difference from the group average nudges a share ~2%
	const maxWeightSwing = 0.35  // ...capped at +/-35%, so no one's cut goes near zero or doubles
	weighted := make([]float64, n)
	weightedTotal := 0.0
	for i, p := range recipients {
		swing := (float64(p.Level) - avgLevel) * perLevelWeight
		if swing > maxWeightSwing {
			swing = maxWeightSwing
		} else if swing < -maxWeightSwing {
			swing = -maxWeightSwing
		}
		weighted[i] = equalShare * (1.0 + swing)
		weightedTotal += weighted[i]
	}

	shares := make([]float64, n)
	for i := range shares {
		shares[i] = weighted[i] * (pool / weightedTotal)
	}

	// Level-spread penalty: shrinks the pool, not any one member's slice of it.
	// <=15 levels apart: full pool. 15-40: tapers 100% -> 50%. 40+: tapers 50% -> a
	// 25% floor at a 60-level spread (so grouping never falls below a quarter pool).
	spread := float64(maxLevel - minLevel)
	penalty := 1.0
	switch {
	case spread <= 15:
		penalty = 1.0
	case spread <= 40:
		penalty = 1.0 - 0.5*(spread-15)/25
	default:
		t := (spread - 40) / 20
		if t > 1 {
			t = 1
		}
		penalty = 0.5 - 0.25*t
	}

	result := make([]int, n)
	for i, s := range shares {
		result[i] = int(s*penalty + 0.5)
		if result[i] < 1 {
			result[i] = 1
		}
	}
	return result
}

// handleMonsterDeath awards kill XP/alignment/loot for a monster's death. recipients[0]
// is the primary killer — room-level effects (weapon drop, treasure, kill-event log) are
// anchored to their room. XP is split evenly across all of recipients (each still gets
// their own per-level diminishing-returns scaling and level-up check), which is a normal
// solo kill when len(recipients) == 1 — every existing call site passes exactly one
// player. Callers with no single decisive blow (bleed-out, tentacle DOT) can instead pass
// a whole group via dotKillRecipients. recipients must be non-empty.
func (e *GameEngine) handleMonsterDeath(recipients []*Player, inst *MonsterInstance, def *gameworld.MonsterDef) {
	if len(recipients) == 0 {
		return
	}
	killer := recipients[0]

	// Take everyone who was fighting this monster out of combat, not just the killer —
	// see clearCombatForMonster for why a group can share the same CombatTarget.MonsterID.
	e.clearCombatForMonster(inst.ID)

	// Some monsters permanently flip a global PVAL on death (e.g. MONSTERS.SCR MNUMBER
	// 294 Ming-K'Tuk: "PVAL 90 1", which DEEP2.SCR room 1811's tribute puzzle checks via
	// "IFVAR PVAL90 = 0" to switch the room to its permanent post-dragon state). Without
	// this, killing such a monster instead of using its intended script-driven solution
	// left the world thinking it was still alive forever.
	if def.DeathPValNum > 0 {
		if e.PVals == nil {
			e.PVals = make(map[int]int)
		}
		e.PVals[def.DeathPValNum] = def.DeathPValValue
		e.savePVals()
	}

	// XP formula: Body (not ExtraBody) + Attack/5 + Defense/5 + Armor/2 + level scaling,
	// split evenly across recipients before each person's own per-level scaling applies.
	baseXP := def.Body + def.Attack1/5 + def.Defense/5 + def.Armor/2
	if def.MagicResist > 0 {
		baseXP += def.MagicResist / 5
	}
	if baseXP < 10 {
		baseXP = 10
	}
	shares := computeGroupXPShares(baseXP, recipients)

	e.Events.Publish("combat", fmt.Sprintf("%s killed %s (monster %d) in room %d",
		killer.FirstName, def.Name, def.Number, killer.RoomNumber))

	// Drop monster's weapon into the room as loot (skip natural weapons like claws/teeth/fists)
	if len(def.Weapons) > 0 && !def.Discorporate {
		room := e.rooms[killer.RoomNumber]
		if room != nil {
			wep := def.Weapons[rand.Intn(len(def.Weapons))]
			wepDef := e.items[wep.Archetype]
			if wepDef != nil && !isNaturalWeapon(wepDef.Type) {
				ref := len(room.Items)
				ri := gameworld.RoomItem{
					Ref:       ref,
					Archetype: wep.Archetype,
					Adj1:      wep.Adj,
				}
				if def.WeaponPlus > 0 {
					ri.Val2 = def.WeaponPlus
				}
				room.Items = append(room.Items, ri)
				wepName := e.formatItemName(wepDef, wep.Adj, 0, 0)
				if e.localRoomBroadcast != nil {
					article := articleFor(wepName, false)
					e.localRoomBroadcast(killer.RoomNumber, []string{fmt.Sprintf("%s%s clatters to the ground.", capArticle(article), wepName)})
				}
			}
		}
	}

	// Generate treasure drops based on monster's TREASURE level
	if def.Treasure > 0 && !def.Discorporate {
		treasureMsgs := e.generateTreasure(killer.RoomNumber, def.Treasure, e.monsterCurrency(inst.ID, killer.RoomNumber))
		if len(treasureMsgs) > 0 && e.localRoomBroadcast != nil {
			e.localRoomBroadcast(killer.RoomNumber, treasureMsgs)
		}
	}

	// GM-staged queued treasure (@queue/@unqueue) — independent of the monster's own
	// TREASURE level, since a GM may want even a throwaway monster to carry a
	// specific staged item for an event or quest.
	if !def.Discorporate {
		if msg, droppedItemName := e.rollQueuedTreasureDrop(killer.RoomNumber); msg != "" {
			if e.localRoomBroadcast != nil {
				e.localRoomBroadcast(killer.RoomNumber, []string{msg})
			}
			if e.gmBroadcast != nil {
				monsterName := FormatMonsterName(def, e.monAdjs)
				monsterPhrase := articleFor(monsterName, def.Unique) + monsterName
				itemPhrase := articleFor(droppedItemName, false) + droppedItemName
				e.gmBroadcast([]string{fmt.Sprintf("*HONK* %s just killed %s and got %s.", killer.FirstName, monsterPhrase, itemPhrase)})
			}
		}
	}

	// Items a GM silently placed into this monster's carried items (@give ... to <monster>)
	if !def.Discorporate {
		e.dropMonsterCarriedItems(killer.RoomNumber, inst)
	}

	for i, p := range recipients {
		xp := shares[i]
		// Diminishing returns for grinding weak-for-your-level content: gated on the
		// monster's full value (baseXP), not this player's post-split share. For a solo
		// kill those are the same number (computeGroupXPShares returns baseXP unchanged
		// for a single recipient), so solo behavior is identical to before. But in a
		// group, comparing the split share would treat sharing a perfectly worthwhile
		// kill several ways as if the monster itself were trivial — e.g. a 313 XP kill
		// split 3 ways left a level-33 player's ~162 share under her own Level*5 (165)
		// threshold and cratered it to 49, even though 313 is well above what's trivial
		// for her level.
		if p.Level > 1 && baseXP < p.Level*5 {
			xp = max(5, xp*50/(p.Level*5))
		}
		p.Experience += xp

		// Alignment shift
		if def.Alignment < 0 {
			p.Alignment += 1
		} else if def.Alignment > 0 {
			p.Alignment -= 1
		}

		// Recalculate build points and check for level-up
		oldBP := p.BuildPoints
		leveledUp := recalcBuildPoints(p)
		newBP := p.BuildPoints

		var xpMsgs []string
		xpMsgs = append(xpMsgs, fmt.Sprintf("[+%d experience]", xp))
		if newBP > oldBP {
			xpMsgs = append(xpMsgs, fmt.Sprintf("[+%d build points! Total: %d]", newBP-oldBP, newBP))
		}

		if leveledUp {
			// Stat bonuses (HP/Fatigue/Mana/Psi) are applied inside recalcBuildPoints
			// itself now — see its doc comment — so only messaging happens here.
			xpMsgs = append(xpMsgs, fmt.Sprintf("Congratulations! You have advanced to level %d!", p.Level))
			if e.roomBroadcast != nil && !p.Disguised {
				e.roomBroadcast(p.RoomNumber, []string{
					fmt.Sprintf("%s has advanced to level %d!", p.FirstName, p.Level),
				})
			}
		}

		if e.sendToPlayer != nil {
			e.sendToPlayer(p.FirstName, xpMsgs)
		}
	}

	// If this was a summoned creature, notify its summoner, stun them, and clear their reference
	if inst.IsSummoned && inst.SummonerName != "" {
		cname := strings.ToLower(def.Name)
		var deathMsg string
		if inst.IsFamiliar {
			deathMsg = fmt.Sprintf("Your familiar, the %s, has been slain!", cname)
		} else {
			deathMsg = fmt.Sprintf("Your summoned %s has been destroyed!", cname)
		}
		stunSecs := 2 + rand.Intn(4) // 2-5 seconds
		e.setWatching(inst.SummonerName, 0)
		if e.sessions != nil {
			for _, p := range e.sessions.OnlinePlayers() {
				if p.FirstName == inst.SummonerName && p.SummonedCreatureID == inst.ID {
					p.SummonedCreatureID = 0
					p.RoundTimeExpiry = time.Now().Add(time.Duration(stunSecs) * time.Second)
					// A real stun, not just round time — see elementalGuardIntercept's caller.
					p.Stunned = true
					p.StunExpiry = p.RoundTimeExpiry
					break
				}
			}
		}
		if e.sendToPlayer != nil {
			e.sendToPlayer(inst.SummonerName, []string{deathMsg, fmt.Sprintf("[Stunned: %d sec]", stunSecs)})
		}
	}
}

// queuedTreasureDropChance is the percentage chance, per monster death (while the
// GM-staged treasure queue is non-empty), that the front item is dispensed. The
// original DOS-era mechanic for this queue isn't documented in the surviving GM
// manual, so this chance — and the fact that it's independent of the monster's own
// TREASURE level — is a judgment call matching the rest of the treasure system's
// feel; tune freely.
const queuedTreasureDropChance = 10

// rollQueuedTreasureDrop checks the GM-staged treasure queue (see @queue/@unqueue
// in gm_commands.go) and, on a successful roll, pops the item at the front (top)
// of the queue and drops it into roomNum. Returns the room message to show and the
// dropped item's display name, or ("", "") if nothing dropped.
func (e *GameEngine) rollQueuedTreasureDrop(roomNum int) (dropMsg, itemName string) {
	e.treasureQueueMu.Lock()
	if len(e.treasureQueue) == 0 || rand.Intn(100) >= queuedTreasureDropChance {
		e.treasureQueueMu.Unlock()
		return "", ""
	}
	item := e.treasureQueue[0]
	e.treasureQueue = e.treasureQueue[1:]
	e.treasureQueueMu.Unlock()

	room := e.rooms[roomNum]
	if room == nil {
		return "", ""
	}
	itemDef := e.items[item.Archetype]
	if itemDef == nil {
		return "", ""
	}
	ri := gameworld.RoomItem{
		Ref:       nextRoomItemRef(room),
		Archetype: item.Archetype,
		Adj1:      item.Adj1, Adj2: item.Adj2, Adj3: item.Adj3,
		Val1: item.Val1, Val2: item.Val2, Val3: item.Val3, Val4: item.Val4, Val5: item.Val5,
		Sharpness:   item.Sharpness,
		HardnessMod: item.HardnessMod,
			CraftedValue: item.CraftedValue,
		ItemBits:    item.ItemBits,
		State:       item.State,
		Extend:      item.Tail,
	}
	if isContainerDef(itemDef) && len(item.Contents) > 0 {
		e.roomContainerSet(roomNum, ri.Ref, item.Contents)
	}
	room.Items = append(room.Items, ri)
	itemName = e.formatItemName(itemDef, item.Adj1, item.Adj2, item.Adj3, item.Tail)
	return fmt.Sprintf("You see %s lies among the remains.", itemName), itemName
}

// dropMonsterCarriedItems drops everything a GM silently staged into inst's carried
// items (via "@give <item> to <monster>", see gmGiveToMonster in gm_commands.go)
// onto the ground when it dies, mirroring the monster's own weapon drop above.
func (e *GameEngine) dropMonsterCarriedItems(roomNum int, inst *MonsterInstance) {
	if len(inst.CarriedItems) == 0 {
		return
	}
	room := e.rooms[roomNum]
	if room == nil {
		return
	}
	var msgs []string
	for _, ii := range inst.CarriedItems {
		itemDef := e.items[ii.Archetype]
		if itemDef == nil {
			continue
		}
		ri := gameworld.RoomItem{
			Ref:       nextRoomItemRef(room),
			Archetype: ii.Archetype,
			Adj1:      ii.Adj1, Adj2: ii.Adj2, Adj3: ii.Adj3,
			Val1: ii.Val1, Val2: ii.Val2, Val3: ii.Val3, Val4: ii.Val4, Val5: ii.Val5,
			Sharpness:   ii.Sharpness,
			HardnessMod: ii.HardnessMod,
			CraftedValue: ii.CraftedValue,
			ItemBits:    ii.ItemBits,
			State:       ii.State,
			Extend:      ii.Tail,
		}
		if isContainerDef(itemDef) && len(ii.Contents) > 0 {
			e.roomContainerSet(roomNum, ri.Ref, ii.Contents)
		}
		room.Items = append(room.Items, ri)
		name := e.formatItemName(itemDef, ii.Adj1, ii.Adj2, ii.Adj3, ii.Tail)
		msgs = append(msgs, fmt.Sprintf("You see %s lies among the remains.", name))
	}
	inst.CarriedItems = nil
	if e.monsterMgr != nil {
		e.monsterMgr.mu.Lock()
		if idx := e.monsterMgr.indexOfID(inst.ID); idx >= 0 {
			e.monsterMgr.instances[idx].CarriedItems = nil
		}
		e.monsterMgr.mu.Unlock()
	}
	if len(msgs) > 0 && e.localRoomBroadcast != nil {
		e.localRoomBroadcast(roomNum, msgs)
	}
}

// ---- Flee ----

// compassDirections are the 8 exit keys FLEE is allowed to pick from — deliberately
// excludes U/D/O (see directionNames): fleeing in a blind panic makes sense as a
// lateral scramble, not a controlled climb/descent/portal step, which staying put
// (or ADVANCE/RETREAT + a deliberate GO) already covers.
var compassDirections = map[string]bool{
	"N": true, "S": true, "E": true, "W": true,
	"NE": true, "NW": true, "SE": true, "SW": true,
}

func (e *GameEngine) doFlee(ctx context.Context, player *Player) *CommandResult {
	if player.CombatTarget == nil && !player.Joined && !e.isEngagedInMelee(player) {
		return &CommandResult{Messages: []string{"You are not in combat."}}
	}
	if player.Dead {
		return &CommandResult{Messages: []string{"You can't flee. You are dead."}}
	}
	if player.Stunned {
		return &CommandResult{Messages: []string{"You are stunned and cannot flee!"}}
	}
	if player.Immobilized {
		return &CommandResult{Messages: []string{"You are rooted to the spot!"}}
	}

	room := e.rooms[player.RoomNumber]
	if room == nil {
		return &CommandResult{Messages: []string{"You have nowhere to flee!"}}
	}

	type exitInfo struct {
		dir    string
		destID int
	}
	var exits []exitInfo
	for dir, dest := range room.Exits {
		if dest > 0 && compassDirections[dir] {
			exits = append(exits, exitInfo{dir, dest})
		}
	}
	if len(exits) == 0 {
		return &CommandResult{Messages: []string{"There is nowhere to flee!"}}
	}

	fleeChance := 50 + player.Quickness/5 + player.Agility/10
	if player.Position != 0 {
		fleeChance -= 20
	}
	if rand.Intn(100) >= fleeChance {
		return &CommandResult{
			Messages:      []string{"You try to flee but can't get away!"},
			RoomBroadcast: []string{fmt.Sprintf("%s tries to flee but fails!", player.DisplayNameCap())},
		}
	}

	chosen := exits[rand.Intn(len(exits))]
	e.breakAllMeleeEngagements(player)

	dirName := directionNames[chosen.dir]
	if dirName == "" {
		dirName = strings.ToLower(chosen.dir)
	}

	oldRoom := player.RoomNumber
	player.RoomNumber = chosen.destID
	// Fleeing while flying (e.g. over an AERIAL room with no ground) must not
	// silently ground the player — that would strand them unable to DESCEND
	// (which requires Position == 4) with no floor to stand on. Every other
	// position (sitting/kneeling/laying) still snaps back up to standing.
	if player.Position != 4 {
		player.Position = 0
	}
	player.Submitting = false

	result := e.doLook(player)
	result.Messages = append([]string{fmt.Sprintf("You flee %s!", dirName)}, result.Messages...)
	result.OldRoom = oldRoom
	result.OldRoomMsg = []string{fmt.Sprintf("%s flees %s!", player.DisplayNameCap(), dirName)}
	result.RoomBroadcast = []string{fmt.Sprintf("%s arrives, looking panicked.", player.DisplayNameCap())}

	return result
}

func (e *GameEngine) disengageCombat(player *Player) {
	if player.CombatTarget != nil && player.CombatTarget.IsMonster {
		e.monsterMgr.mu.Lock()
		for i := range e.monsterMgr.instances {
			if e.monsterMgr.instances[i].ID == player.CombatTarget.MonsterID {
				if e.monsterMgr.instances[i].Target == player.FirstName {
					e.monsterMgr.instances[i].Target = ""
				}
				break
			}
		}
		e.monsterMgr.mu.Unlock()
	}
	player.CombatTarget = nil
	player.Joined = false
}

// clearCombatForMonster takes every online player whose CombatTarget still points at
// monsterID out of combat. A monster's death only reaches the killer/caster passed into
// handleMonsterDeath — this catches everyone else who was also fighting the same monster
// (a group fight, or anyone who joined via ATTACK — see the ATTACK handler, which only
// sets a monster's own Target back-reference if it was empty, so multiple players can
// share the same CombatTarget.MonsterID) so they don't have to notice and RETREAT manually.
func (e *GameEngine) clearCombatForMonster(monsterID int) {
	if e.sessions == nil {
		return
	}
	for _, p := range e.sessions.OnlinePlayers() {
		if p.CombatTarget != nil && p.CombatTarget.IsMonster && p.CombatTarget.MonsterID == monsterID {
			p.CombatTarget = nil
			p.Joined = false
		}
		// A dead monster must come out of every player's AdvancedOnMonsters list,
		// not just whoever's CombatTarget pointed at it — ADVANCE doesn't require
		// ATTACK (a player could have advanced without ever landing a hit, or two
		// players both advanced and only one's CombatTarget was set). Left stale,
		// isEngagedInMelee stays true forever (the ID never matches a live monster
		// again), forcing RETREAT even once nothing is alive.
		if len(p.AdvancedOnMonsters) > 0 {
			for i, id := range p.AdvancedOnMonsters {
				if id == monsterID {
					p.AdvancedOnMonsters = append(p.AdvancedOnMonsters[:i], p.AdvancedOnMonsters[i+1:]...)
					break
				}
			}
		}
	}
}

// ---- Stances ----

func (e *GameEngine) doStance(player *Player, stance int) *CommandResult {
	player.Stance = stance
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You adopt a %s combat stance.", stanceNames[stance])},
		RoomBroadcast: []string{fmt.Sprintf("%s adopts a %s combat stance.", player.DisplayNameCap(), stanceNames[stance])},
	}
}

// ---- Search Dead Monster for Loot ----

func (e *GameEngine) doSearchMonster(ctx context.Context, player *Player, args []string) *CommandResult {
	rawTarget := strings.ToLower(strings.Join(args, " "))
	target, ordSkip := parseOrdinal(rawTarget)

	if e.monsterMgr == nil {
		return nil
	}

	matchCount := 0
	monsters := e.monsterMgr.AllMonstersInRoom(player.RoomNumber)
	for _, inst := range monsters {
		if inst.Alive {
			continue // can only search dead monsters
		}
		def := e.monsters[inst.DefNumber]
		if def == nil {
			continue
		}
		name := strings.ToLower(FormatMonsterName(def, e.monAdjs))
		noun := strings.ToLower(def.Name)
		if !strings.HasPrefix(name, target) && !strings.HasPrefix(noun, target) {
			continue
		}
		matchCount++
		if matchCount <= ordSkip {
			continue
		}

		// Check if already searched — mark via monsterMgr and remove corpse immediately.
		e.monsterMgr.mu.Lock()
		idx := e.monsterMgr.indexOfID(inst.ID)
		if idx >= 0 && e.monsterMgr.instances[idx].Searched {
			e.monsterMgr.mu.Unlock()
			return &CommandResult{Messages: []string{fmt.Sprintf("You have already searched the %s.", def.Name)}}
		}
		if idx >= 0 {
			e.monsterMgr.instances[idx].Searched = true
			// Remove the corpse from the room immediately.
			roomNum := e.monsterMgr.instances[idx].RoomNumber
			roomIndices := e.monsterMgr.monstersByRoom[roomNum]
			for j, ridx := range roomIndices {
				if ridx == idx {
					e.monsterMgr.monstersByRoom[roomNum] = append(roomIndices[:j], roomIndices[j+1:]...)
					break
				}
			}
			e.monsterMgr.instances[idx].DeathTime = time.Time{} // prevent cleanupCorpses from processing again
		}
		e.monsterMgr.mu.Unlock()

		displayName := FormatMonsterName(def, e.monAdjs)
		var msgs []string
		msgs = append(msgs, fmt.Sprintf("You search %s%s.", articleFor(displayName, def.Unique), displayName))

		// Treasure based on monster's Treasure level
		if def.Treasure > 0 {
			// Generate coins based on treasure level
			copperAmount := rand.Intn(def.Treasure*20) + def.Treasure*5
			// In a currency region the corpse may carry the local money
			// instead (currency.go), at the same value.
			if md := e.monsterCurrency(inst.ID, player.RoomNumber); md != nil {
				amount := copperToCurrency(md, copperAmount)
				addFunds(player, md, amount)
				msgs = append(msgs, fmt.Sprintf("You find %s.", formatCurrency(md, amount)))
				searchRT := applyRoundTime(player, 5)
				player.RoundTimeExpiry = time.Now().Add(time.Duration(searchRT) * time.Second)
				msgs = append(msgs, fmt.Sprintf(" [Round: %d sec]", searchRT))
				e.SavePlayer(ctx, player)
				return &CommandResult{
					Messages:      msgs,
					RoomBroadcast: []string{fmt.Sprintf("%s searches %s%s.", player.DisplayNameCap(), articleFor(displayName, def.Unique), displayName)},
					PlayerState:   player,
				}
			}
			gold := copperAmount / 100
			silver := (copperAmount % 100) / 10
			copper := copperAmount % 10

			var found []string
			if gold > 0 {
				player.Gold += gold
				found = append(found, fmt.Sprintf("%d gold", gold))
			}
			if silver > 0 {
				player.Silver += silver
				found = append(found, fmt.Sprintf("%d silver", silver))
			}
			if copper > 0 {
				player.Copper += copper
				found = append(found, fmt.Sprintf("%d copper", copper))
			}
			if len(found) > 0 {
				msgs = append(msgs, fmt.Sprintf("You find %s.", joinWithAnd(found)))
			} else {
				msgs = append(msgs, "You find nothing.")
			}
		} else {
			msgs = append(msgs, "You find nothing.")
		}

		// Search roundtime
		searchRT := applyRoundTime(player, 5)
		player.RoundTimeExpiry = time.Now().Add(time.Duration(searchRT) * time.Second)
		msgs = append(msgs, fmt.Sprintf(" [Round: %d sec]", searchRT))

		e.SavePlayer(ctx, player)
		return &CommandResult{
			Messages:      msgs,
			RoomBroadcast: []string{fmt.Sprintf("%s searches %s%s.", player.DisplayNameCap(), articleFor(displayName, def.Unique), displayName)},
			PlayerState:   player,
		}
	}

	return nil // not a dead monster — fall through to normal SEARCH
}

// ---- Monster Guard Behavior ----

// findGuardFor finds a guard monster for the given target monster in the same room.
func (e *GameEngine) findGuardFor(target *MonsterInstance, roomNum int) (*MonsterInstance, *gameworld.MonsterDef) {
	if e.monsterMgr == nil {
		return nil, nil
	}
	targetDef := e.monsters[target.DefNumber]
	if targetDef == nil {
		return nil, nil
	}

	e.monsterMgr.mu.RLock()
	defer e.monsterMgr.mu.RUnlock()

	for i := range e.monsterMgr.instances {
		inst := &e.monsterMgr.instances[i]
		if inst.RoomNumber != roomNum || !inst.Alive || inst.ID == target.ID {
			continue
		}
		// A summoned or controlled creature (e.g. an undead dominated via Control
		// Undead) is under its player's command now, not acting as an independent
		// guardian of its own kind — it must not intercept attacks on its own behalf.
		if inst.IsSummoned {
			continue
		}
		def := e.monsters[inst.DefNumber]
		if def == nil {
			continue
		}
		if def.GuardItem == target.DefNumber {
			return inst, def
		}
	}
	return nil, nil
}

// ---- Cry for Law ----

func (e *GameEngine) cryForLaw(attacker *Player, target *MonsterInstance, targetDef *gameworld.MonsterDef) {
	// Find guard/sentry type monsters in nearby rooms and aggro them
	if e.monsterMgr == nil || e.localRoomBroadcast == nil {
		return
	}
	name := FormatMonsterName(targetDef, e.monAdjs)
	e.localRoomBroadcast(attacker.RoomNumber, []string{fmt.Sprintf("%s%s cries out for help!", capArticle(articleFor(name, targetDef.Unique)), name)})

	// Alert guards in the same room
	e.monsterMgr.mu.Lock()
	defer e.monsterMgr.mu.Unlock()
	for i := range e.monsterMgr.instances {
		inst := &e.monsterMgr.instances[i]
		if inst.RoomNumber != attacker.RoomNumber || !inst.Alive || inst.Target != "" || inst.ID == target.ID {
			continue
		}
		def := e.monsters[inst.DefNumber]
		if def == nil {
			continue
		}
		// Guards/sentries (strategy 101-200) will defend
		if def.Strategy >= 101 && def.Strategy <= 200 {
			inst.Target = attacker.FirstName
			if containsInt(attacker.AdvancedOnMonsters, inst.ID) {
				continue // attacker already closed to melee with this guard
			}
			guardName := FormatMonsterName(def, e.monAdjs)
			guardArticle := capArticle(articleFor(guardName, def.Unique))
			// Same "advances towards" wording as ordinary hostile aggro (monsters.go)
			// and the player ADVANCE command — see shirla.cap. Previously this was a
			// attacker-only "turns toward you with hostile intent!" line that nobody
			// else in the room ever saw.
			if e.sendToPlayer != nil {
				e.sendToPlayer(attacker.FirstName, []string{fmt.Sprintf("%s%s advances towards you.", guardArticle, guardName)})
			}
			e.broadcastCombatRoom(inst.RoomNumber, attacker, []string{fmt.Sprintf("%s%s advances towards %s.", guardArticle, guardName, attacker.DisplayName())})
		}
	}
}

// summonedAttackMonster executes one combat tick where a summoned creature attacks a monster.
// Must be called with monsterMgr.mu held; unlocks/relocks around the broadcast.
func (e *GameEngine) summonedAttackMonster(inst *MonsterInstance, def *gameworld.MonsterDef) {
	tIdx := e.monsterMgr.indexOfID(inst.MonsterTargetID)
	if tIdx < 0 {
		inst.MonsterTargetID = 0
		return
	}
	target := &e.monsterMgr.instances[tIdx]
	if !target.Alive || target.RoomNumber != inst.RoomNumber {
		inst.MonsterTargetID = 0
		return
	}
	targetDef := e.monsters[target.DefNumber]
	if targetDef == nil {
		inst.MonsterTargetID = 0
		return
	}

	// Build names
	aName := strings.ToLower(FormatMonsterName(def, e.monAdjs))
	aArt := articleFor(aName, def.Unique)
	tName := strings.ToLower(FormatMonsterName(targetDef, e.monAdjs))
	tArt := articleFor(tName, targetDef.Unique)

	monVerb, _ := monsterAttackVerb(def, e.items)
	monWeapon := e.monsterWeaponName(def)
	attackLine := fmt.Sprintf("%s%s %s %s%s with its %s.", capArticle(aArt), aName, monVerb, tArt, tName, monWeapon)

	effAtk := monsterEffectiveAttack(def, inst)
	toHit := calcToHit(effAtk, monsterEffectiveDefense(targetDef, target))
	roll := rand.Intn(100) + 1

	var msgs []string
	if roll < toHit {
		msgs = []string{attackLine, fmt.Sprintf("[ToHit: %d, Roll: %d] Miss!", toHit, roll)}
	} else {
		hitLabel := "Hit!"
		if roll >= 96 {
			hitLabel = "Excellent Hit!"
		}
		// MagicWeapon immunity
		if targetDef.MagicWeapon > 0 && def.WeaponPlus < targetDef.MagicWeapon {
			texI := targetDef.TextOverrides["TEXI"]
			if texI == "" {
				texI = fmt.Sprintf("%s%s's attack has no effect on %s%s.", capArticle(aArt), aName, tArt, tName)
			}
			msgs = []string{attackLine, fmt.Sprintf("[ToHit: %d, Roll: %d] %s", toHit, roll, hitLabel), texI}
		} else {
			dmg := monsterDamageForAttack(effAtk)
			dmg = applyArmor(dmg, targetDef.Armor)
			var attackerWeaponDef *gameworld.ItemDef
			if len(def.Weapons) > 0 {
				attackerWeaponDef = e.items[def.Weapons[0].Archetype]
			}
			if level, ok := targetDef.Immunities[weaponImmunityType(attackerWeaponDef)]; ok {
				dmg = applyImmunity(dmg, level)
			}
			if dmg <= 0 {
				dmg = 1
			}
			target.CurrentHP -= dmg
			hitLine := fmt.Sprintf("[ToHit: %d, Roll: %d] %s %s%s takes %d damage.", toHit, roll, hitLabel, tArt, tName, dmg)
			if target.CurrentHP <= 0 {
				target.Alive = false
				target.DeathTime = time.Now()
				inst.MonsterTargetID = 0
				texD := targetDef.TextOverrides["TEXD"]
				suffix := " It is destroyed!"
				if texD != "" {
					suffix = " It " + texD
				}
				msgs = []string{attackLine, hitLine + suffix}
				// Award XP/loot/treasure to the summoner exactly like a normal kill, and
				// notify them — previously this only sent the flavor-text notification
				// below with no XP at all, so a controlled/summoned creature's kills were
				// worthless to level with (reported: "if they kill something, I get no
				// experience"). handleMonsterDeath does a fair amount of work (loot, room
				// broadcasts, possible Events.Publish subscribers) so it's called with
				// monsterMgr.mu released, same as the sendToPlayer call already was.
				if inst.SummonerName != "" && e.sessions != nil {
					for _, p := range e.sessions.OnlinePlayers() {
						if p.FirstName == inst.SummonerName {
							e.monsterMgr.mu.Unlock()
							e.handleMonsterDeath(e.sharedXPRecipients(p), target, targetDef)
							if e.sendToPlayer != nil {
								e.sendToPlayer(inst.SummonerName, []string{fmt.Sprintf("Your %s has slain %s%s!", aName, tArt, tName)})
							}
							e.monsterMgr.mu.Lock()
							break
						}
					}
				}
			} else {
				msgs = []string{attackLine, hitLine}
			}
		}
	}

	if e.localRoomBroadcast != nil && len(msgs) > 0 {
		roomNum := inst.RoomNumber
		e.monsterMgr.mu.Unlock()
		e.localRoomBroadcast(roomNum, msgs)
		e.monsterMgr.mu.Lock()
	}
}

// ---- Monster Combat AI ----

func (e *GameEngine) monsterCombatTick(inst *MonsterInstance, def *gameworld.MonsterDef) {
	if !inst.Alive {
		return
	}

	// Expire timed status effects
	now := time.Now()
	if inst.Sleeping && now.After(inst.SleepExpiry) {
		inst.Sleeping = false
		inst.SleepStand = true
		if e.localRoomBroadcast != nil {
			name := FormatMonsterName(def, e.monAdjs)
			e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("The %s stirs and wakes up.", strings.ToLower(name))})
		}
	}
	if inst.Webbed && now.After(inst.WebExpiry) {
		inst.Webbed = false
		if e.localRoomBroadcast != nil {
			name := FormatMonsterName(def, e.monAdjs)
			e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("The %s breaks free of the webs.", strings.ToLower(name))})
		}
	}
	if inst.Tentacled && now.After(inst.TentacleExpiry) {
		inst.Tentacled = false
		if e.localRoomBroadcast != nil {
			name := FormatMonsterName(def, e.monAdjs)
			e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("The tentacles release the %s and sink back into the ground.", strings.ToLower(name))})
		}
	}
	if inst.Feared && now.After(inst.FearExpiry) {
		inst.Feared = false
	}
	if inst.Charmed && now.After(inst.CharmExpiry) {
		inst.Charmed = false
		inst.CharmTarget = ""
	}
	if inst.Silenced && now.After(inst.SilenceExpiry) {
		inst.Silenced = false
	}
	if inst.Imprisoned && now.After(inst.ImprisonExpiry) {
		inst.Imprisoned = false
		if e.localRoomBroadcast != nil {
			name := FormatMonsterName(def, e.monAdjs)
			e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("The force bubble around the %s shimmers and fades.", strings.ToLower(name))})
		}
	}
	if inst.Stunned && now.After(inst.StunExpiry) {
		inst.Stunned = false
		if e.localRoomBroadcast != nil {
			name := FormatMonsterName(def, e.monAdjs)
			e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("The %s shakes off its stupor.", strings.ToLower(name))})
		}
	}
	if inst.KnockedDown && now.After(inst.KnockedDownExpiry) {
		inst.KnockedDown = false
		if e.localRoomBroadcast != nil {
			name := FormatMonsterName(def, e.monAdjs)
			e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("The %s scrambles back to its feet!", strings.ToLower(name))})
		}
	}

	// Monster attacking another monster — summoned creatures via COMMAND ATTACK, or a
	// GUARDIAN-flagged monster (e.g. the large wolf) defending players from a hostile.
	// summonedAttackMonster itself doesn't require IsSummoned; only its kill-notification
	// branch checks SummonerName, which is naturally empty for a non-summoned attacker.
	// KnockedDown/Stunned clearing (with its "scrambles back to its feet" broadcast) is
	// handled once, uniformly, by the expiry block above — this just withholds the
	// attack while any of them are still in effect.
	if inst.MonsterTargetID > 0 {
		if !inst.Sleeping && !inst.Webbed && !inst.Tentacled && !inst.Stunned && !inst.KnockedDown {
			e.summonedAttackMonster(inst, def)
		}
		return
	}

	if inst.Target == "" {
		return
	}

	// Sleeping: no action at all
	if inst.Sleeping {
		return
	}

	// Waking up: skip one tick to stand, then resume
	if inst.SleepStand {
		inst.SleepStand = false
		if e.localRoomBroadcast != nil {
			name := FormatMonsterName(def, e.monAdjs)
			e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("The %s stands erect, looking furious!", strings.ToLower(name))})
		}
		return
	}

	// Webbed: cannot attack or flee
	if inst.Webbed {
		return
	}

	// Tentacled: cannot attack or flee
	if inst.Tentacled {
		return
	}

	// Stunned: cannot attack, move, or flee until StunExpiry passes (expired above)
	if inst.Stunned {
		return
	}

	// Knocked down: cannot attack, move, or flee until KnockedDownExpiry passes
	// (expired, with its "scrambles back to its feet" broadcast, above).
	if inst.KnockedDown {
		return
	}

	// Charmed: clear target if aimed at the charmer
	if inst.Charmed && inst.Target == inst.CharmTarget {
		inst.Target = ""
		return
	}

	// Imprisoned: trapped in a force bubble, cannot attack or cast at all
	if inst.Imprisoned {
		return
	}

	if e.sessions == nil {
		return
	}

	// Continue or resolve an in-progress spell cast before anything else this tick —
	// see monsterTryStartCast/resolveMonsterCast. A cast in progress fully occupies the
	// monster (no melee, no starting a second cast) until CASTLEVEL seconds pass or it's
	// disrupted by taking damage (damageMonster).
	if inst.Casting {
		if now.Before(inst.CastExpiry) {
			return
		}
		e.monsterMgr.mu.Unlock()
		playerMsgs, roomMsgs, defender := e.resolveMonsterCast(inst, def)
		e.monsterMgr.mu.Lock()
		// Public first, then the private outcome detail — roomMsgs carries the
		// gesture/hit-flavor announcement and playerMsgs the damage/dodge line. The
		// defender is excluded from the room broadcast since they already got the
		// private detail; without that they'd see the same hit described twice.
		e.broadcastCombatRoom(inst.RoomNumber, defender, roomMsgs)
		if e.sendToPlayer != nil && len(playerMsgs) > 0 && defender != nil {
			e.sendToPlayer(defender.FirstName, playerMsgs)
		}
		if e.db != nil && defender != nil {
			go e.SavePlayer(context.Background(), defender)
		}
		return
	}

	// Continue or resolve an in-progress psi attack before anything else this tick —
	// see monsterTryStartPsi/resolveMonsterPsi. Mirrors the spellcasting block above;
	// a psi attack in progress fully occupies the monster the same way a spell cast
	// does, and can likewise be disrupted by taking damage (damageMonster).
	if inst.PsiCasting {
		if now.Before(inst.PsiCastExpiry) {
			return
		}
		e.monsterMgr.mu.Unlock()
		playerMsgs, roomMsgs, defender := e.resolveMonsterPsi(inst, def)
		e.monsterMgr.mu.Lock()
		e.broadcastCombatRoom(inst.RoomNumber, defender, roomMsgs)
		if e.sendToPlayer != nil && len(playerMsgs) > 0 && defender != nil {
			e.sendToPlayer(defender.FirstName, playerMsgs)
		}
		if e.db != nil && defender != nil {
			go e.SavePlayer(context.Background(), defender)
		}
		return
	}

	var target *Player
	for _, p := range e.sessions.OnlinePlayers() {
		if p.FirstName == inst.Target && p.RoomNumber == inst.RoomNumber && !p.Dead {
			target = p
			break
		}
	}

	if target == nil || target.IsConcealed() {
		inst.Target = ""
		return
	}

	// Feared: skip attack entirely, just flee
	if inst.Feared {
		e.monsterFlee(inst, def)
		return
	}

	e.monsterMgr.mu.Unlock()
	// Silenced monsters can't incant a spell unless SILENCEIGNORE (GMSCRIPT.DOC:
	// "the Silence spell will not affect the creature's ability to cast a spell").
	startedCast := false
	if !inst.Silenced || def.SilenceIgnore {
		startedCast = e.monsterTryStartCast(inst, def)
	}
	// Psi attacks aren't gated by Silenced — psionics are a mental discipline, not an
	// incantation (see monsterTryStartPsi).
	startedPsi := false
	if !startedCast {
		startedPsi = e.monsterTryStartPsi(inst, def)
	}
	var playerMsgs, roomMsgs []string
	var defender *Player
	handled := startedCast || startedPsi
	if !handled {
		if specMsgs, specRoomMsgs, specDefender, used := e.monsterTrySpecialAttack(inst, def); used {
			playerMsgs, roomMsgs, defender = specMsgs, specRoomMsgs, specDefender
			handled = true
		}
	}
	if !handled {
		playerMsgs, roomMsgs, defender = e.monsterAttackPlayer(inst, def, target)
	}
	e.monsterMgr.mu.Lock()

	// defender is who actually took the attack — it differs from target when
	// the attack was redirected to a guard, and the detailed [ToHit/Roll]
	// message (playerMsgs) must go to them, not the original target. Public
	// broadcast goes first, excluding defender (who gets the private detail
	// instead of seeing the same hit described twice).
	e.broadcastCombatRoom(inst.RoomNumber, defender, roomMsgs)
	if e.sendToPlayer != nil && len(playerMsgs) > 0 && defender != nil {
		e.sendToPlayer(defender.FirstName, playerMsgs)
	}

	// Save player state after monster combat (persists HP loss, death, poison, etc.)
	if e.db != nil && defender != nil {
		go e.SavePlayer(context.Background(), defender)
	}

	// Monster flee behavior (strategy 301-500 = flee when wounded, 501+ = fight to death)
	if inst.Alive && inst.CurrentHP > 0 {
		hpPct := inst.CurrentHP * 100 / max(1, def.Body+def.ExtraBody)
		shouldFlee := false
		switch {
		case def.Strategy >= 501:
			// Fight to death — never flee
		case def.Strategy >= 301 && def.Strategy < 500:
			shouldFlee = hpPct < 30
		case def.Strategy >= 201 && def.Strategy < 300:
			shouldFlee = hpPct < 50
		case def.Strategy >= 1 && def.Strategy < 200:
			shouldFlee = hpPct < 60
		}
		if shouldFlee {
			e.monsterFlee(inst, def)
		}
	}
}

func (e *GameEngine) monsterFlee(inst *MonsterInstance, def *gameworld.MonsterDef) {
	room := e.rooms[inst.RoomNumber]
	if room == nil {
		return
	}
	type exitInfo struct {
		dir    string
		destID int
	}
	var exits []exitInfo
	for dir, dest := range room.Exits {
		if dest > 0 {
			exits = append(exits, exitInfo{dir, dest})
		}
	}
	if len(exits) == 0 {
		return
	}
	chosen := exits[rand.Intn(len(exits))]
	name := FormatMonsterName(def, e.monAdjs)
	dirName := directionNames[chosen.dir]
	if dirName == "" {
		dirName = strings.ToLower(chosen.dir)
	}
	if dirName == "above" {
		return
	}
	fleeText := def.TextOverrides["TEXF"]
	if fleeText != "" {
		e.localRoomBroadcast(inst.RoomNumber, []string{fleeText + " " + dirName + "."})
	} else {
		article := articleFor(name, def.Unique)
		e.localRoomBroadcast(inst.RoomNumber, []string{fmt.Sprintf("%s%s flees %s!", capArticle(article), name, dirName)})
	}

	inst.Target = ""
	e.monsterMgr.moveMonster(e.monsterMgr.indexOfID(inst.ID), chosen.destID)

	// The monster is gone from the room — take everyone who was fighting it out of
	// combat too, same as on death (see clearCombatForMonster), so they don't stay
	// stuck with a stale CombatTarget pointed at a monster that's no longer here.
	e.clearCombatForMonster(inst.ID)
}

// aggroDelay is how long a hostile monster waits after noticing a player before it
// advances — on room entry (scheduleEntryAggro) and in monsterTick's PendingAggro.
// Fixed, not randomized: a random 1-2s range sometimes landed near 1s (logged at
// 1141ms), too short for a player to read the room and walk back out. Even a flat
// 2s felt too quick in play — a player passing through has to read the room text
// and exit list, then type a direction — so it's 3s.
const aggroDelay = 3 * time.Second

// scheduleEntryAggro runs monsterCheckAggro after the entry delay, skipping it if the
// player has left the room (or logged off) in the meantime.
func (e *GameEngine) scheduleEntryAggro(player *Player, roomNum int) {
	player.RoomEntrySeq++
	seq := player.RoomEntrySeq
	// Any monster that already "noticed" this player (monsterTick's PendingAggro)
	// did so on an earlier visit somewhere. Left in place, its deadline has long
	// passed, so walking back into that room got an instant advance on the next tick.
	if e.monsterMgr != nil {
		e.monsterMgr.mu.Lock()
		for i := range e.monsterMgr.instances {
			delete(e.monsterMgr.instances[i].PendingAggro, player.FirstName)
		}
		e.monsterMgr.mu.Unlock()
	}
	time.AfterFunc(aggroDelay, func() {
		defer recoverBackground("entry aggro")
		// Same room isn't enough: the player may have left and come back, and this
		// timer belongs to the earlier visit — the new visit scheduled its own.
		if player.RoomNumber != roomNum || player.RoomEntrySeq != seq {
			return
		}
		if e.sessions != nil {
			online := false
			for _, p := range e.sessions.OnlinePlayers() {
				if p == player {
					online = true
					break
				}
			}
			if !online {
				return
			}
		}
		e.monsterCheckAggro(player, roomNum)
	})
}

func (e *GameEngine) monsterCheckAggro(player *Player, roomNum int) {
	if e.monsterMgr == nil || player.Dead || player.IsConcealed() {
		return
	}

	e.monsterMgr.mu.Lock()
	defer e.monsterMgr.mu.Unlock()

	for i := range e.monsterMgr.instances {
		inst := &e.monsterMgr.instances[i]
		if inst.RoomNumber != roomNum || !inst.Alive || inst.Sedated || inst.Target != "" || inst.IsSummoned {
			continue
		}
		def := e.monsters[inst.DefNumber]
		if def == nil || def.Strategy < 301 || def.Guardian {
			continue
		}
		inst.Target = player.FirstName
		if containsInt(player.AdvancedOnMonsters, inst.ID) {
			break // player already closed to melee with it — nothing to announce
		}
		name := FormatMonsterName(def, e.monAdjs)
		article := capArticle(articleFor(name, def.Unique))
		// Same "advances towards" wording as monsters.go's periodic hostile-aggro
		// tick and the player ADVANCE command — see shirla.cap. Previously this was
		// a target-only "stands erect and closes with you." line that nobody else
		// in the room ever saw.
		if e.sendToPlayer != nil {
			e.sendToPlayer(player.FirstName, []string{fmt.Sprintf("%s%s advances towards you.", article, name)})
		}
		e.broadcastCombatRoom(inst.RoomNumber, player, []string{fmt.Sprintf("%s%s advances towards %s.", article, name, player.DisplayName())})
		break
	}
}

// ---- Helpers ----

func articleFor(name string, unique bool) string {
	if unique {
		return ""
	}
	if len(name) > 0 && strings.ContainsRune("aeiouAEIOU", rune(name[0])) {
		return "an "
	}
	return "a "
}

func capArticle(article string) string {
	if len(article) == 0 {
		return ""
	}
	return strings.ToUpper(article[:1]) + article[1:]
}

// isNaturalWeapon returns true for body-part weapons that shouldn't drop as loot.
func isNaturalWeapon(itemType string) bool {
	switch itemType {
	case "CLAW_WEAPON", "BITE_WEAPON", "FIST_WEAPON", "CHARGE_WEAPON":
		return true
	}
	return false
}

func (mm *monsterManager) indexOfID(id int) int {
	for i := range mm.instances {
		if mm.instances[i].ID == id {
			return i
		}
	}
	return -1
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
