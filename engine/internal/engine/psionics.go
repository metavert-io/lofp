package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// PsiDiscipline defines a psionic discipline.
type PsiDiscipline struct {
	ID       int
	Name     string
	School   string // "Mind over Matter" or "Mind over Mind"
	Level    int
	PsiCost  int
	RoundSec int    // roundtime seconds
	Effect   string // "damage", "defense", "heal", "buff", "utility"
	DmgMin   int
	DmgMax   int
	DmgType  string
	DefBonus int
	HealMin  int
	HealMax  int
}

var psiRegistry []PsiDiscipline

func init() {
	mom := []PsiDiscipline{
		{1, "Kinetic Thrust", "Mind over Matter", 1, 3, 5, "damage", 4, 15, "crushing", 0, 0, 0},
		{2, "Levitate", "Mind over Matter", 2, 4, 5, "buff", 0, 0, "", 0, 0, 0},
		{3, "Pyrokinetics", "Mind over Matter", 3, 6, 5, "damage", 8, 25, "heat", 0, 0, 0},
		{4, "Cryokinetics", "Mind over Matter", 4, 8, 5, "damage", 8, 25, "cold", 0, 0, 0},
		{5, "Capacitance", "Mind over Matter", 5, 2, 5, "utility", 0, 0, "", 0, 0, 0},
		{6, "Electrify", "Mind over Matter", 6, 10, 5, "damage", 10, 30, "electric", 0, 0, 0},
		{7, "Strengthen Steel", "Mind over Matter", 7, 8, 5, "buff", 0, 0, "", 0, 0, 0},
		{8, "Manipulate Lock", "Mind over Matter", 8, 6, 5, "utility", 0, 0, "", 0, 0, 0},
		{9, "Wall of Force", "Mind over Matter", 9, 12, 5, "defense", 0, 0, "", 25, 0, 0},
		{10, "Flight", "Mind over Matter", 10, 10, 5, "buff", 0, 0, "", 0, 0, 0},
		// ID 42 (not 16) matches LEGENDS.DOC's own discipline number for Speed —
		// unlike the rest of this slice, which renumbers sequentially from 1.
		{42, "Speed", "Mind over Matter", 12, 12, 5, "buff", 0, 0, "", 0, 0, 0},
		{11, "Call on Inner Power", "Mind over Matter", 13, 0, 20, "utility", 0, 0, "", 0, 0, 0},
		{12, "Teleportation", "Mind over Matter", 18, 15, 5, "utility", 0, 0, "", 0, 0, 0},
		{13, "Force Field", "Mind over Matter", 20, 20, 5, "defense", 0, 0, "", 75, 0, 0},
		{14, "Immobilize", "Mind over Matter", 22, 18, 5, "utility", 0, 0, "", 0, 0, 0},
		{15, "Ethereal Projection", "Mind over Matter", 25, 25, 10, "buff", 0, 0, "", 0, 0, 0},
		// Transform Steel is LEGENDS.DOC discipline #13, but internal ID 13 is
		// already Force Field (this slice renumbers sequentially from 1, skipping
		// the unimplemented #1 Matter Awakening and this originally-unimplemented
		// #13) — 16 is simply the next free internal ID, not a doc number (unlike
		// Speed's 42 above, there's no in-game "psi <n>" number on record to match).
		{16, "Transform Steel", "Mind over Matter", 15, 15, 5, "utility", 0, 0, "", 0, 0, 0},
	}
	moo := []PsiDiscipline{
		{50, "Telepathy", "Mind over Mind", 1, 2, 5, "utility", 0, 0, "", 0, 0, 0},
		{51, "Contact", "Mind over Mind", 2, 3, 5, "utility", 0, 0, "", 0, 0, 0},
		{52, "Psychic Probe", "Mind over Mind", 3, 5, 5, "utility", 0, 0, "", 0, 0, 0},
		{53, "Psychic Blast", "Mind over Mind", 4, 8, 0, "damage", 5, 20, "", 0, 0, 0},
		{54, "Psychic Screen", "Mind over Mind", 5, 6, 5, "defense", 0, 0, "", 15, 0, 0},
		{55, "Psychic Crush", "Mind over Mind", 11, 14, 0, "damage", 10, 35, "", 0, 0, 0},
		{56, "Terror", "Mind over Mind", 15, 16, 0, "damage", 8, 25, "", 0, 0, 0},
		{57, "Psychic Shield", "Mind over Mind", 10, 10, 5, "defense", 0, 0, "", 25, 0, 0},
		{58, "Psychic Barrier", "Mind over Mind", 16, 14, 5, "defense", 0, 0, "", 35, 0, 0},
		{59, "Confuse", "Mind over Mind", 13, 12, 0, "utility", 0, 0, "", 0, 0, 0},
		{60, "Focus Skill", "Mind over Mind", 14, 8, 5, "buff", 0, 0, "", 0, 0, 0},
		{61, "Domination", "Mind over Mind", 18, 20, 5, "utility", 0, 0, "", 0, 0, 0},
		{62, "Disruption", "Mind over Mind", 19, 16, 0, "utility", 0, 0, "", 0, 0, 0},
		{63, "Psychic Fortress", "Mind over Mind", 20, 18, 5, "defense", 0, 0, "", 50, 0, 0},
		{64, "Warp Mind", "Mind over Mind", 22, 22, 0, "utility", 0, 0, "", 0, 0, 0},
		{65, "Pain", "Mind over Mind", 21, 20, 0, "damage", 20, 50, "", 0, 0, 0},
	}
	psiRegistry = append(psiRegistry, mom...)
	psiRegistry = append(psiRegistry, moo...)
}

// FindPsiByName finds a discipline by prefix match.
func FindPsiByName(input string) *PsiDiscipline {
	input = strings.ToLower(input)
	for i := range psiRegistry {
		if strings.ToLower(psiRegistry[i].Name) == input {
			return &psiRegistry[i]
		}
	}
	var match *PsiDiscipline
	for i := range psiRegistry {
		if strings.HasPrefix(strings.ToLower(psiRegistry[i].Name), input) {
			if match != nil {
				return nil
			}
			match = &psiRegistry[i]
		}
	}
	return match
}

// FindPsiByID returns a discipline by ID.
func FindPsiByID(id int) *PsiDiscipline {
	for i := range psiRegistry {
		if psiRegistry[i].ID == id {
			return &psiRegistry[i]
		}
	}
	return nil
}

// doPreparePsi handles PSI [discipline#|name].
// With no args: lists known disciplines and active maintained powers.
// With a number or name: activates the discipline. For maintained powers, toggles on/off.
func (e *GameEngine) doPreparePsi(player *Player, args []string) *CommandResult {
	if player.Dead {
		return &CommandResult{Messages: []string{"You can't use psionics while dead."}}
	}

	// Check base psionic skill
	if player.Skills[26] == 0 && !player.IsGM {
		return &CommandResult{Messages: []string{"You have no training in Psionics."}}
	}

	// Initialize ActivePsi map if nil
	if player.ActivePsi == nil {
		player.ActivePsi = make(map[int]bool)
	}

	// No args: list disciplines
	if len(args) == 0 {
		return e.listPsiDisciplines(player)
	}

	// Try to find discipline by number first, then by name
	input := strings.Join(args, " ")
	var disc *PsiDiscipline
	var discNum int
	if _, err := fmt.Sscanf(args[0], "%d", &discNum); err == nil {
		disc = FindPsiByID(discNum)
	}
	if disc == nil {
		disc = FindPsiByName(input)
	}
	if disc == nil {
		return &CommandResult{Messages: []string{fmt.Sprintf("You don't know a discipline called '%s'.", input)}}
	}

	// Check if this is a maintained power that's already active — toggle off
	isMaintained := disc.Effect == "buff" || disc.Effect == "defense"
	if isMaintained && player.ActivePsi[disc.ID] {
		delete(player.ActivePsi, disc.ID)
		// Remove defense bonus if applicable
		if disc.DefBonus > 0 {
			player.DefenseBonus -= disc.DefBonus
		}
		// Clear flight state. Over open sky that means falling (skyfall.go),
		// not "standing" in mid-air.
		var fallMsgs []string
		if disc.ID == 2 || disc.ID == 10 { // Levitate or Flight
			player.CanFly = false
			fallMsgs = e.fallIfFlightLost(context.Background(), player, true)
			if fallMsgs == nil && player.Position == 4 && !player.IsFlying() {
				player.Position = 0
			}
		}
		// Clear ethereal projection
		if disc.ID == 15 {
			player.EtherealActive = false
			player.Hidden = false
		}
		// Speed shares the same HasteExpiry field applyRoundTime reads for every
		// timed spell-Haste — clearing it here also cancels the round-time discount.
		if disc.ID == 42 {
			player.HasteExpiry = time.Time{}
			return &CommandResult{
				Messages:      []string{"You stop maintaining Speed.", "You slow down considerably."},
				RoomBroadcast: []string{fmt.Sprintf("%s returns to normal speed.", player.FirstName)},
			}
		}
		if fallMsgs != nil {
			// The fall already told both rooms; a RoomBroadcast here would go to
			// the landing room, which never saw the concentration being released.
			return &CommandResult{Messages: append([]string{fmt.Sprintf("You release your concentration on %s.", disc.Name)}, fallMsgs...)}
		}
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You release your concentration on %s.", disc.Name)},
			RoomBroadcast: []string{fmt.Sprintf("%s relaxes %s concentration.", player.FirstName, player.Possessive())},
		}
	}

	if player.Psi < disc.PsiCost {
		return &CommandResult{Messages: []string{fmt.Sprintf("Not enough psi points. (%s costs %d, you have %d)", disc.Name, disc.PsiCost, player.Psi)}}
	}

	// Instantaneous powers that don't need a target activate immediately
	if isMaintained {
		player.Psi -= disc.PsiCost

		// Speed (42) is the one maintained MoM buff with a real chance of failure —
		// per the skill-check shape used elsewhere in this file (see doProjectPsi and
		// psiResistRoll): base 50 + psi/school skill*3 + Willpower/5, penalized by the
		// discipline's level since it's a freshly-learned power at minimum skill.
		if disc.ID == 42 {
			psiSkill := player.Skills[26]
			momSkill := player.Skills[28]
			chance := 50 + (psiSkill+momSkill)*3 + player.Willpower/5 - disc.Level*2
			if chance < 15 {
				chance = 15
			}
			if chance > 95 {
				chance = 95
			}
			if player.IsGM {
				chance = 100
			}
			roll := rand.Intn(100) + 1
			if roll > chance {
				return &CommandResult{
					Messages:      []string{"You concentrate...", fmt.Sprintf("[Success: %d%%, Roll %d] Failure.", chance, roll), "You fail to reach the necessary focus."},
					RoomBroadcast: []string{fmt.Sprintf("%s concentrates intently, but nothing happens.", player.FirstName)},
				}
			}
			player.ActivePsi[disc.ID] = true
			// Speed is maintained (toggled), not timed like the Haste spell, so it
			// sets HasteExpiry far in the future rather than for a fixed duration.
			// Both share the same field applyRoundTime reads, so this gets the same
			// halved round time as spell Haste for free; toggling off zeroes it again.
			player.HasteExpiry = time.Now().Add(10 * 365 * 24 * time.Hour)
			psiRT := applyRoundTime(player, disc.RoundSec)
			player.RoundTimeExpiry = time.Now().Add(time.Duration(psiRT) * time.Second)
			return &CommandResult{
				Messages: []string{
					"You concentrate...",
					fmt.Sprintf("[Success: %d%%, Roll %d] Success!", chance, roll),
					"You move considerably faster.",
					fmt.Sprintf(" [Round: %d sec]", psiRT),
				},
				RoomBroadcast: []string{
					fmt.Sprintf("%s concentrates intently.", player.FirstName),
					fmt.Sprintf("%s begins moving with blurring speed.", player.FirstName),
				},
			}
		}

		player.ActivePsi[disc.ID] = true
		if disc.DefBonus > 0 {
			player.DefenseBonus += disc.DefBonus
		}
		var msg string
		switch disc.ID {
		case 2: // Levitate
			msg = "You feel yourself becoming lighter."
		case 7: // Strengthen Steel
			msg = "You focus your will into your weapon."
		case 10: // Flight
			msg = "You rise into the air."
			player.Position = 4 // flying
			player.CanFly = true
		case 9: // Wall of Force
			msg = "A shimmering wall of force surrounds you."
		case 13: // Force Field
			msg = "A powerful force field envelops you."
		case 15: // Ethereal Projection
			msg = "Your body becomes translucent as you shift into the ethereal plane."
			player.Hidden = true
			player.EtherealActive = true
		case 54: // Psychic Screen
			msg = "A psychic screen forms around your mind."
		case 57: // Psychic Shield
			msg = "A psychic shield protects your mind."
		case 58: // Psychic Barrier
			msg = "A psychic barrier reinforces your defenses."
		case 60: // Focus Skill
			msg = "You sharpen your mental focus."
		case 63: // Psychic Fortress
			msg = "An impenetrable psychic fortress surrounds your mind."
		default:
			msg = fmt.Sprintf("You activate %s.", disc.Name)
		}
		result := &CommandResult{
			Messages:      []string{msg},
			RoomBroadcast: []string{fmt.Sprintf("%s concentrates intently.", player.FirstName)},
		}
		if disc.RoundSec > 0 {
			psiRT := applyRoundTime(player, disc.RoundSec)
			player.RoundTimeExpiry = time.Now().Add(time.Duration(psiRT) * time.Second)
			result.Messages = append(result.Messages, fmt.Sprintf(" [Round: %d sec]", psiRT))
		}
		return result
	}

	// Offensive/utility powers that need a target: prepare for PROJECT
	player.PreparedPsi = disc.ID
	if disc.RoundSec > 0 {
		psiRT2 := applyRoundTime(player, disc.RoundSec)
		player.RoundTimeExpiry = time.Now().Add(time.Duration(psiRT2) * time.Second)
	}

	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You focus your mind on %s... (type PROJECT to release, or PROJECT <target>)", disc.Name)},
		RoomBroadcast: []string{fmt.Sprintf("%s closes %s eyes in concentration.", player.FirstName, player.Possessive())},
	}
}

// listPsiDisciplines shows all known disciplines and their active status.
func (e *GameEngine) listPsiDisciplines(player *Player) *CommandResult {
	var msgs []string
	msgs = append(msgs, "Your psionic disciplines:")
	msgs = append(msgs, "")

	// Determine which disciplines the player knows based on skill levels
	momLevel := player.Skills[28] // Mind over Matter
	mooLevel := player.Skills[27] // Mind over Mind
	psiLevel := player.Skills[26] // base Psionics

	if psiLevel == 0 && !player.IsGM {
		return &CommandResult{Messages: []string{"You have no training in Psionics."}}
	}

	msgs = append(msgs, fmt.Sprintf("%-4s %-25s %-6s %-6s %s", "#", "Discipline", "Cost", "School", "Status"))
	msgs = append(msgs, fmt.Sprintf("%-4s %-25s %-6s %-6s %s", "--", "----------", "----", "------", "------"))

	for _, disc := range psiRegistry {
		// Check if player has enough skill to know this discipline
		var skillLevel int
		if disc.School == "Mind over Matter" {
			skillLevel = momLevel
		} else {
			skillLevel = mooLevel
		}
		if skillLevel < disc.Level && !player.IsGM {
			continue
		}

		status := ""
		if player.ActivePsi != nil && player.ActivePsi[disc.ID] {
			status = "[ACTIVE]"
		}
		if player.PreparedPsi == disc.ID {
			status = "[PREPARED]"
		}

		schoolAbbr := "MoMat"
		if disc.School == "Mind over Mind" {
			schoolAbbr = "MoMnd"
		}

		msgs = append(msgs, fmt.Sprintf("%-4d %-25s %-6d %-6s %s", disc.ID, disc.Name, disc.PsiCost, schoolAbbr, status))
	}

	return &CommandResult{Messages: msgs}
}

// doProjectPsi handles PROJECT [target].
func (e *GameEngine) doProjectPsi(ctx context.Context, player *Player, args []string) *CommandResult {
	// Give a carried item's own universal IFPREVERB PROJECT -1 script (e.g. item
	// 925, "the object") a chance to hijack the attempt before the psionics
	// preconditions below — the item's reaction isn't tied to having a discipline
	// prepared, so it needs to run before that check can ever reject the target.
	if len(args) > 0 {
		if room := e.rooms[player.RoomNumber]; room != nil {
			target := strings.ToLower(strings.Join(args, " "))
			for _, ii := range player.Inventory {
				itemDef := e.items[ii.Archetype]
				if itemDef == nil {
					continue
				}
				name := e.getItemNounName(itemDef)
				if !matchesTarget(name, target, e.getAdjName(ii.Adj1), e.getAdjName(ii.Adj2), e.getAdjName(ii.Adj3)) {
					continue
				}
				if res := e.runItemOwnPreverbHook(player, room, "PROJECT", ii); res != nil {
					return res
				}
			}
		}
	}
	if player.Dead {
		return &CommandResult{Messages: []string{"You can't use psionics while dead."}}
	}
	if player.PreparedPsi == 0 {
		return &CommandResult{Messages: []string{"You have no discipline prepared. Use PSI <discipline> first."}}
	}

	disc := FindPsiByID(player.PreparedPsi)
	if disc == nil {
		player.PreparedPsi = 0
		return &CommandResult{Messages: []string{"Your focus dissipates."}}
	}

	if player.Psi < disc.PsiCost {
		player.PreparedPsi = 0
		return &CommandResult{Messages: []string{fmt.Sprintf("Not enough psi points! (%s requires %d, you have %d)", disc.Name, disc.PsiCost, player.Psi)}}
	}

	// Check roundtime
	if disc.RoundSec > 0 && player.RoundTimeExpiry.After(time.Now()) {
		remaining := player.RoundTimeExpiry.Sub(time.Now()).Seconds()
		return &CommandResult{Messages: []string{fmt.Sprintf("You are still focusing... %.0f seconds remaining.", remaining+0.5)}}
	}

	// Deduct psi — keep discipline prepared for re-use (psi retains last power)
	player.Psi -= disc.PsiCost

	// Skill check
	psiSkill := player.Skills[26]
	schoolSkill := 0
	if disc.School == "Mind over Matter" {
		schoolSkill = player.Skills[28]
	} else {
		schoolSkill = player.Skills[27]
	}
	castChance := 50 + (psiSkill+schoolSkill)*3 + player.Willpower/5 - disc.Level*2
	if castChance < 15 {
		castChance = 15
	}
	if player.IsGM {
		castChance = 100
	}
	psiRoll := rand.Intn(100)
	if psiRoll >= castChance {
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You project %s but fail to focus the energy!", disc.Name)},
			RoomBroadcast: []string{fmt.Sprintf("%s concentrates intensely but nothing happens.", player.FirstName)},
		}
	}

	result := &CommandResult{}

	switch disc.Effect {
	case "damage":
		result = e.projectDamage(player, disc, args, psiRoll, castChance)
	case "defense":
		player.DefenseBonus += disc.DefBonus
		result.Messages = []string{fmt.Sprintf("You project %s! (+%d defense)", disc.Name, disc.DefBonus)}
		result.RoomBroadcast = []string{fmt.Sprintf("%s concentrates and a shimmering barrier appears.", player.FirstName)}
	case "buff":
		result = e.projectBuff(player, disc)
	case "utility":
		if disc.ID == 14 { // Immobilize
			result = e.projectImmobilize(player, args)
		} else if disc.ID == 12 { // Teleportation
			result = e.projectTeleport(ctx, player, args)
		} else if disc.ID == 8 { // Manipulate Lock
			result = e.projectManipulateLock(player, args)
		} else if disc.ID == 16 { // Transform Steel
			result = e.projectTransformSteel(player)
		} else {
			result.Messages = []string{fmt.Sprintf("You project %s.", disc.Name)}
			result.RoomBroadcast = []string{fmt.Sprintf("%s concentrates intensely.", player.FirstName)}
		}
	default:
		result.Messages = []string{fmt.Sprintf("You project %s.", disc.Name)}
		result.RoomBroadcast = []string{fmt.Sprintf("%s concentrates intensely.", player.FirstName)}
	}

	if disc.RoundSec > 0 {
		player.RoundTimeExpiry = time.Now().Add(time.Duration(disc.RoundSec) * time.Second)
	}
	e.SavePlayer(ctx, player)

	return result
}

func (e *GameEngine) projectImmobilize(player *Player, args []string) *CommandResult {
	if len(args) == 0 {
		return &CommandResult{Messages: []string{"Immobilize whom?"}}
	}
	targetName := strings.Join(args, " ")

	// Try monster first
	inst, def := e.findMonsterInRoom(player, targetName)
	if inst != nil {
		inst.Sedated = true // use Sedated to prevent movement/wandering
		name := FormatMonsterName(def, e.monAdjs)
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You project Immobilize at %s! Invisible force bands wrap around it, freezing it in place.", name)},
			RoomBroadcast: []string{fmt.Sprintf("%s concentrates and %s freezes in place.", player.FirstName, name)},
		}
	}

	// Try player
	found := e.findPlayerInRoom(player, targetName)
	if found != nil {
		found.Immobilized = true
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You project Immobilize at %s! Invisible force bands wrap around them.", found.FirstName)},
			RoomBroadcast: []string{fmt.Sprintf("%s concentrates and %s freezes in place.", player.FirstName, found.FirstName)},
			TargetName:    found.FirstName,
			TargetMsg:     []string{"Invisible force bands wrap around you, making it impossible to move!"},
		}
	}

	return &CommandResult{Messages: []string{fmt.Sprintf("You don't see %s here.", targetName)}}
}

// psiResisted reports whether a monster's psionic resistance stops a
// discipline from landing on it. Like MagicResist, PSIRESIST is a rating
// (tens to thousands), not a 0-100 percentage — weighed against the caster's
// psi rating (same skill+school+Willpower shape as the psi cast-success check
// above) using calcToHit's rating-vs-rating shape to size a penalty. That
// penalty is subtracted from castChance — the same PROJECT cast-chance
// doProjectPsi already rolled psiRoll against once — and psiRoll is
// re-checked against that lowered bar, rather than drawing an independent
// second roll. Mirrors magicResisted's identical fix in spells.go: the
// target's resistance is a modifier on the one roll already made, not a
// second, separate chance to resist stacked on top of it.
//
// calcToHit's raw return value is centered on 50 for a neutral (equal-rating)
// matchup, so using it directly as "penalty" baked in an unconditional -50 tax
// even when the caster's rating matched the monster's resist exactly — see
// magicResisted's doc comment for the full rationale. Subtracting the neutral
// baseline back out (calcToHit(...)-50) makes a matched rating contribute zero
// penalty, so this is actually a modifier on the one roll rather than a much
// harsher roll hiding underneath it.
//
// GMs always bypass this entirely, mirroring magicResisted: their cast chance
// is a hardcoded 100% specifically so GM testing/admin casts never fail for
// any reason, and a resisted cast is exactly as much a failure as a missed
// cast-success roll.
func psiResisted(player *Player, disc *PsiDiscipline, resist, psiRoll, castChance int) bool {
	if player.IsGM || resist <= 0 {
		return false
	}
	psiSkill := player.Skills[26]
	schoolSkill := player.Skills[27]
	if disc.School == "Mind over Matter" {
		schoolSkill = player.Skills[28]
	}
	casterRating := 50 + (psiSkill+schoolSkill)*3 + player.Willpower/5
	penalty := calcToHit(casterRating, resist) - 50
	adjusted := castChance - penalty
	if adjusted < 15 {
		adjusted = 15
	}
	return psiRoll >= adjusted
}

// playerPsiResistRoll reports whether a player resists an offensive psionic
// discipline a monster uses against them (see resolveMonsterPsi in combat.go) — the
// reverse direction of psiResisted above, and still an independent roll since a
// monster's own "cast chance" isn't modeled the way a player caster's is (there's
// no equivalent single roll to fold this into). Per GMSCRIPT.DOC, PSISKILL is "the
// total chance that the psionic creature has when invoking a psionic power...
// Powerful psionic creatures should have well over 100 so as to defeat a player's
// natural resistance to psionics" — so every player has some baseline resistance
// regardless of training, which Cloak Mind (spell 235, "+25 psi resistance" per
// MAGIC.TXT) adds to. Mirrors magicResisted's rating-vs-rating shape (spells.go):
// monsterPsiSkill is the "attack" side, the player's resistance rating is the
// "defense" side.
func playerPsiResistRoll(target *Player, monsterPsiSkill int) bool {
	if monsterPsiSkill <= 0 {
		return false
	}
	resistRating := 25 + target.Willpower/5 + cloakMindResistBonus(target)
	return rand.Intn(100) < calcToHit(monsterPsiSkill, resistRating)
}

func (e *GameEngine) projectDamage(player *Player, disc *PsiDiscipline, args []string, psiRoll, castChance int) *CommandResult {
	targetName := ""
	if len(args) > 0 {
		targetName = strings.Join(args, " ")
	} else if player.CombatTarget != nil && player.CombatTarget.IsMonster {
		e.monsterMgr.mu.RLock()
		for _, inst := range e.monsterMgr.instances {
			if inst.ID == player.CombatTarget.MonsterID && inst.Alive {
				def := e.monsters[inst.DefNumber]
				if def != nil {
					targetName = def.Name
				}
			}
		}
		e.monsterMgr.mu.RUnlock()
	}
	if targetName == "" {
		return &CommandResult{Messages: []string{"Project at what? Specify a target."}}
	}

	inst, def := e.findMonsterInRoom(player, targetName)
	if inst == nil {
		// Try player target
		found := e.findPlayerInRoom(player, targetName)
		if found != nil {
			return &CommandResult{Messages: []string{fmt.Sprintf("You can't project %s at %s. Psionic attacks against players are not yet implemented.", disc.Name, found.FirstName)}}
		}
		return &CommandResult{Messages: []string{fmt.Sprintf("You don't see '%s' here.", targetName)}}
	}
	name := FormatMonsterName(def, e.monAdjs)

	dmg := rand.Intn(disc.DmgMax-disc.DmgMin+1) + disc.DmgMin

	// Psi resistance
	resist := def.PsiResist
	if resist <= 0 {
		resist = def.MagicResist
	}
	if psiResisted(player, disc, resist, psiRoll, castChance) {
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You project %s at a %s, but it resists!", disc.Name, name)},
			RoomBroadcast: []string{fmt.Sprintf("%s concentrates at a %s, but it resists!", player.FirstName, name)},
			Resisted:      true,
		}
	}

	// Elemental immunity
	if disc.DmgType != "" {
		immType := elementalImmunityType(disc.DmgType)
		if level, ok := def.Immunities[immType]; ok {
			dmg = applyImmunity(dmg, level)
		}
	}

	if dmg <= 0 {
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You project %s at a %s, but it seems unaffected!", disc.Name, name)},
			RoomBroadcast: []string{fmt.Sprintf("%s concentrates at a %s!", player.FirstName, name)},
		}
	}

	killed, _ := e.damageMonster(inst.ID, dmg, player.FirstName)
	if killed {
		deathText := def.TextOverrides["TEXD"]
		deathMsg := fmt.Sprintf("A %s collapses, dead!", name)
		if deathText != "" {
			deathMsg = fmt.Sprintf("A %s %s", name, deathText)
		}
		e.handleMonsterDeath(e.sharedXPRecipients(player), inst, def)
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You project %s at a %s for %d damage!", disc.Name, name, dmg), deathMsg},
			RoomBroadcast: []string{fmt.Sprintf("%s focuses psychic energy at a %s!", player.FirstName, name), deathMsg},
		}
	}
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You project %s at a %s for %d damage!", disc.Name, name, dmg)},
		RoomBroadcast: []string{fmt.Sprintf("%s focuses psychic energy at a %s!", player.FirstName, name)},
	}
}

func (e *GameEngine) projectBuff(player *Player, disc *PsiDiscipline) *CommandResult {
	msg := fmt.Sprintf("You project %s.", disc.Name)
	switch disc.ID {
	case 2: // Levitate
		player.CanFly = true
		msg = "You project Levitate. You feel lighter and begin to float."
	case 7: // Strengthen Steel
		msg = "You project Strengthen Steel. Your weapon gleams with psychic energy. (+15 weapon bonus)"
	case 10: // Flight
		player.CanFly = true
		msg = "You project Flight. You rise into the air!"
	case 15: // Ethereal Projection
		player.Hidden = true
		player.EtherealActive = true
		msg = "You project Ethereal Projection. Your body becomes translucent."
	case 60: // Focus Skill
		msg = "You project Focus Skill. Your mind sharpens. (+25 to next skill roll)"
	}
	return &CommandResult{
		Messages:      []string{msg},
		RoomBroadcast: []string{fmt.Sprintf("%s concentrates intensely.", player.FirstName)},
	}
}

// projectTeleport teleports the player to a marked location.
func (e *GameEngine) projectTeleport(ctx context.Context, player *Player, args []string) *CommandResult {
	if player.Marks == nil || len(player.Marks) == 0 {
		return &CommandResult{Messages: []string{"You have no marks set. Use MARK <1-10> to mark a location first."}}
	}
	markNum := 1
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > 10 {
			return &CommandResult{Messages: []string{"Mark number must be 1-10."}}
		}
		markNum = n
	}
	roomNum, ok := player.Marks[markNum]
	if !ok {
		return &CommandResult{Messages: []string{fmt.Sprintf("Mark %d is not set.", markNum)}}
	}
	room := e.rooms[roomNum]
	if room == nil {
		return &CommandResult{Messages: []string{"That mark leads to a room that no longer exists."}}
	}
	oldRoom := player.RoomNumber
	player.RoomNumber = roomNum
	e.SavePlayer(ctx, player)
	lookResult := e.doLook(player)
	result := &CommandResult{
		Messages:      append([]string{"You project Teleportation! The world blurs around you..."}, lookResult.Messages...),
		RoomBroadcast: []string{fmt.Sprintf("%s appears in a flash of psionic energy.", player.FirstName)},
		RoomName:      lookResult.RoomName,
		RoomDesc:      lookResult.RoomDesc,
		Exits:         lookResult.Exits,
		Items:         lookResult.Items,
	}
	result.OldRoom = oldRoom
	result.OldRoomMsg = []string{fmt.Sprintf("%s vanishes in a flash of psionic energy!", player.FirstName)}
	return result
}

// projectManipulateLock attempts to psionically unlock a locked item in the room.
func (e *GameEngine) projectManipulateLock(player *Player, args []string) *CommandResult {
	room := e.rooms[player.RoomNumber]
	if room == nil {
		return &CommandResult{Messages: []string{"You can't do that here."}}
	}
	target := ""
	if len(args) > 0 {
		target = strings.ToLower(strings.Join(args, " "))
	}
	// Find first locked item (or matching target)
	for i, ri := range room.Items {
		itemDef := e.items[ri.Archetype]
		if itemDef == nil || ri.State != "LOCKED" {
			continue
		}
		name := e.getItemNounName(itemDef)
		if target != "" && !matchesTarget(name, target, e.getAdjName(ri.Adj1), e.getAdjName(ri.Adj2), e.getAdjName(ri.Adj3)) {
			continue
		}
		// Skill check: psi skill + willpower vs lock difficulty
		psiSkill := player.Skills[26] + player.Skills[28]
		lockDiff := ri.Val1
		chance := 40 + psiSkill*3 + player.Willpower/5 - lockDiff
		if chance < 10 {
			chance = 10
		}
		if player.IsGM {
			chance = 100
		}
		displayName := e.formatItemName(itemDef, ri.Adj1, ri.Adj2, ri.Adj3, ri.Extend)
		if rand.Intn(100) < chance {
			room.Items[i].State = "CLOSED"
			e.notifyRoomChange(RoomChange{RoomNumber: player.RoomNumber, Type: "item_state", ItemRef: ri.Ref, NewState: "CLOSED"})
			return &CommandResult{
				Messages:      []string{fmt.Sprintf("You concentrate on %s... The lock clicks open!", displayName)},
				RoomBroadcast: []string{fmt.Sprintf("%s stares intently at %s.", player.FirstName, displayName)},
			}
		}
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You concentrate on %s but the lock resists your mental force.", displayName)},
			RoomBroadcast: []string{fmt.Sprintf("%s stares intently at %s.", player.FirstName, displayName)},
		}
	}
	return &CommandResult{Messages: []string{"You don't see anything locked here."}}
}

// transformSteelMetals are the exotic metals Transform Steel can turn a steel
// weapon into — the same three "exotic" grades weaponsmithing already treats
// as rarer/pricier than plain steel (see the ore-grade weights in crafting.go).
var transformSteelMetals = []string{"elkyri", "randar", "truesteel"}

// projectTransformSteel handles PROJECT for Transform Steel (LEGENDS.DOC #13):
// "Alters steel at the molecular level to another metal. This will only work
// on weapons, and the results are unpredictable. The transformed weapon will
// usually have a bonus in the range of 3-17." Permanently swaps whichever
// Adj1/Adj2/Adj3 slot holds "steel" for a random exotic metal and imprints a
// random 3-17 magical to-hit bonus into VAL2 — always succeeds once the
// preconditions are met (no documented resistance/failure roll), mirroring
// castChargeWandSpell's "always works if the target qualifies" shape. Refuses
// a weapon that already carries a VAL2 bonus (already magically enchanted, by
// this discipline or anything else) rather than overwriting it, and refuses
// anything that isn't a wielded steel weapon in the first place.
func (e *GameEngine) projectTransformSteel(player *Player) *CommandResult {
	if player.Wielded == nil {
		return &CommandResult{Messages: []string{"You aren't wielding anything."}}
	}
	weaponDef := e.items[player.Wielded.Archetype]
	if weaponDef == nil || !isWeapon(weaponDef.Type) {
		return &CommandResult{Messages: []string{"You aren't wielding a weapon."}}
	}
	if !e.isSteelWeapon(player.Wielded) {
		itemName := e.formatItemName(weaponDef, player.Wielded.Adj1, player.Wielded.Adj2, player.Wielded.Adj3, player.Wielded.Tail)
		return &CommandResult{Messages: []string{fmt.Sprintf("%s is not made of steel and is not suitable for transforming.", capitalize(itemName))}}
	}
	if player.Wielded.Val2 > 0 {
		itemName := e.formatItemName(weaponDef, player.Wielded.Adj1, player.Wielded.Adj2, player.Wielded.Adj3, player.Wielded.Tail)
		return &CommandResult{Messages: []string{fmt.Sprintf("%s already hums with magical energy and is not suitable for transforming.", capitalize(itemName))}}
	}

	oldName := e.formatItemName(weaponDef, player.Wielded.Adj1, player.Wielded.Adj2, player.Wielded.Adj3, player.Wielded.Tail)

	steelAdj := e.adjByName("steel")
	newAdj := e.adjByName(transformSteelMetals[rand.Intn(len(transformSteelMetals))])
	switch steelAdj {
	case player.Wielded.Adj1:
		player.Wielded.Adj1 = newAdj
	case player.Wielded.Adj2:
		player.Wielded.Adj2 = newAdj
	case player.Wielded.Adj3:
		player.Wielded.Adj3 = newAdj
	}
	bonus := rand.Intn(15) + 3 // 3-17 inclusive
	player.Wielded.Val2 = bonus

	newName := e.formatItemName(weaponDef, player.Wielded.Adj1, player.Wielded.Adj2, player.Wielded.Adj3, player.Wielded.Tail)

	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You project Transform Steel at %s! It shudders and reshapes at the molecular level, emerging as %s. (+%d magical bonus)", oldName, newName, bonus)},
		RoomBroadcast: []string{fmt.Sprintf("%s concentrates intensely, and %s weapon shudders and reshapes!", player.DisplayNameCap(), player.Possessive())},
	}
}
