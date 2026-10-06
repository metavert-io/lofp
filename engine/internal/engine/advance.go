package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// weaponNeedsAdvance reports whether attacking with this weapon (nil meaning bare
// hands/Martial Arts, or natural claws from Claw Growth) requires the attacker to
// have closed to melee range first (see doAdvance/doAttackMonster). Exempt:
// POLE_WEAPON and DRAKIN_POLE (long reach), THROWN_WEAPON and BOW_WEAPON, plus
// their close cousins already grouped as "ranged" elsewhere in combat.go
// (isRangedWeaponType) — STABTHROWN, POLETHROWN, DRAKIN_THROWN, HANDGUN, RIFLE.
// Note POLE_WEAPON/DRAKIN_POLE are deliberately NOT in isRangedWeaponType (they're
// not thrown/fired, just long), so this can't simply delegate to it. Everything
// else, including unarmed combat, needs to ADVANCE first.
func weaponNeedsAdvance(weaponType string, hasWeapon bool) bool {
	if !hasWeapon {
		return true // bare hands / Martial Arts / natural claws: melee only
	}
	switch weaponType {
	case "POLE_WEAPON", "DRAKIN_POLE":
		return false
	}
	return !isRangedWeaponType(weaponType)
}

// guardianOf returns the online player in target's room who is guarding target
// (Combat Maneuvering's GUARD command, player_commands.go), or nil if target
// isn't guarded. See isPlayerGuarded for the bool-only variant.
func (e *GameEngine) guardianOf(target *Player) *Player {
	if e.sessions == nil {
		return nil
	}
	for _, p := range e.sessions.OnlinePlayers() {
		if p.RoomNumber != target.RoomNumber || p.FirstName == target.FirstName {
			continue
		}
		if containsString(p.GuardTargets, target.FirstName) {
			return p
		}
	}
	return nil
}

// isPlayerGuarded reports whether another online player in the same room is
// currently guarding target (Combat Maneuvering's GUARD command, player_commands.go)
// — per skills.txt, a guarded player is protected from being approached in melee.
func (e *GameEngine) isPlayerGuarded(target *Player) bool {
	return e.guardianOf(target) != nil
}

// isEngagedInMelee reports whether player currently has an active melee-range
// engagement in their current room — either direction: something they've
// ADVANCEd on, something that has ADVANCEd on them, or a hostile monster that has
// closed to melee with them (MonsterInstance.Target, set by monster AI aggro as
// well as ATTACK — see monsters.go). While true, GO/portal travel/CLIMB/FLY are
// blocked and RETREAT is required first.
func (e *GameEngine) isEngagedInMelee(player *Player) bool {
	if len(player.AdvancedOnMonsters) > 0 || len(player.AdvancedOnPlayers) > 0 || len(player.AdvancedByPlayers) > 0 {
		return true
	}
	if e.monsterMgr != nil {
		e.monsterMgr.mu.RLock()
		for i := range e.monsterMgr.instances {
			inst := &e.monsterMgr.instances[i]
			if inst.Alive && inst.RoomNumber == player.RoomNumber && inst.Target == player.FirstName {
				e.monsterMgr.mu.RUnlock()
				return true
			}
		}
		e.monsterMgr.mu.RUnlock()
	}
	return false
}

// meleeBlockMessage returns the message to show a player whose GO/CLIMB/portal/FLY
// attempt is blocked by an active melee engagement, or "" if they aren't engaged.
func (e *GameEngine) meleeBlockMessage(player *Player) string {
	if e.isEngagedInMelee(player) {
		return "You are engaged in melee combat! You must RETREAT first."
	}
	return ""
}

// doAdvance handles ADVANCE <target>: closes to melee range with a monster or
// player in the room, required before attacking with a non-reach melee weapon
// (see weaponNeedsAdvance) unless the target has already closed to melee with the
// attacker instead (see isEngagedInMelee/doAttackMonster).
func (e *GameEngine) doAdvance(ctx context.Context, player *Player, args []string) *CommandResult {
	if len(args) == 0 {
		return &CommandResult{Messages: []string{"Advance on whom?"}}
	}
	if player.Dead {
		return &CommandResult{Messages: []string{"You can't do that. You are dead."}}
	}
	if player.Stunned {
		return &CommandResult{Messages: []string{"You are stunned and cannot move!"}}
	}
	if player.Position == 2 {
		return &CommandResult{Messages: []string{"You can't advance while laying down! Stand up first."}}
	}
	if player.Immobilized {
		return &CommandResult{Messages: []string{"You are rooted to the spot!"}}
	}
	if len(player.Entangles) > 0 {
		return &CommandResult{Messages: []string{fmt.Sprintf("You are bound by %s and cannot move!", player.Entangles[0].SpellName)}}
	}
	if player.IsImprisoned() {
		return &CommandResult{Messages: []string{"You are trapped within a blue force bubble and cannot move!"}}
	}

	target := strings.ToLower(strings.Join(args, " "))

	// Try monster first.
	inst, def := e.findMonsterInRoom(player, target)
	if inst != nil {
		// A guard monster (GUARDITEM, see findGuardFor) steps in front of its
		// charge — the advance lands on the guard instead, mirroring how
		// doAttackMonster redirects an attack to the guard.
		var blockedMsg, blockedBroadcast string
		if guardInst, guardDef := e.findGuardFor(inst, player.RoomNumber); guardInst != nil && guardDef != nil {
			origName := FormatMonsterName(def, e.monAdjs)
			origArticle := articleFor(origName, def.Unique)
			guardName := FormatMonsterName(guardDef, e.monAdjs)
			guardArticle := articleFor(guardName, guardDef.Unique)
			blockedMsg = fmt.Sprintf("%s%s blocks your way to %s%s!", capArticle(guardArticle), guardName, origArticle, origName)
			blockedBroadcast = fmt.Sprintf("%s%s blocks %s's way to %s%s!", capArticle(guardArticle), guardName, player.DisplayName(), origArticle, origName)
			inst, def = guardInst, guardDef
		}
		if inst.Imprisoned {
			name := FormatMonsterName(def, e.monAdjs)
			article := articleFor(name, def.Unique)
			return &CommandResult{Messages: []string{fmt.Sprintf("A shimmering force bubble protects %s%s -- you cannot approach!", article, name)}}
		}
		name := FormatMonsterName(def, e.monAdjs)
		article := articleFor(name, def.Unique)
		// Already engaged either way: we advanced on it, or it already advanced
		// on us (monster aggro sets inst.Target — see isEngagedInMelee). Closing
		// the same gap a second time is meaningless.
		if containsInt(player.AdvancedOnMonsters, inst.ID) || inst.Target == player.FirstName {
			if blockedMsg != "" {
				return &CommandResult{Messages: []string{blockedMsg, fmt.Sprintf("You are already engaging %s%s.", article, name)}}
			}
			return &CommandResult{Messages: []string{fmt.Sprintf("You are already engaging %s%s.", article, name)}}
		}
		player.AdvancedOnMonsters = append(player.AdvancedOnMonsters, inst.ID)
		if blockedMsg != "" {
			return &CommandResult{
				Messages:      []string{blockedMsg, fmt.Sprintf("You advance towards %s%s instead.", article, name)},
				RoomBroadcast: []string{blockedBroadcast, fmt.Sprintf("%s advances towards %s%s instead.", player.DisplayNameCap(), article, name)},
			}
		}
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You advance towards %s%s.", article, name)},
			RoomBroadcast: []string{fmt.Sprintf("%s advances towards %s%s.", player.DisplayNameCap(), article, name)},
		}
	}

	// Then a player.
	found := e.findPlayerInRoom(player, target)
	if found != nil {
		if found.IsImprisoned() {
			return &CommandResult{Messages: []string{fmt.Sprintf("A shimmering force bubble protects %s -- you cannot approach!", found.DisplayName())}}
		}
		if e.isPlayerGuarded(found) {
			return &CommandResult{Messages: []string{fmt.Sprintf("%s is being guarded and cannot be approached.", found.DisplayNameCap())}}
		}
		if containsString(player.AdvancedOnPlayers, found.FirstName) || containsString(player.AdvancedByPlayers, found.FirstName) {
			return &CommandResult{Messages: []string{fmt.Sprintf("You are already engaging %s.", found.DisplayName())}}
		}
		player.AdvancedOnPlayers = append(player.AdvancedOnPlayers, found.FirstName)
		found.AdvancedByPlayers = append(found.AdvancedByPlayers, player.FirstName)
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You advance towards %s.", found.DisplayName())},
			RoomBroadcast: []string{fmt.Sprintf("%s advances towards %s.", player.DisplayNameCap(), found.DisplayName())},
		}
	}

	return &CommandResult{Messages: []string{fmt.Sprintf("You don't see '%s' here.", strings.Join(args, " "))}}
}

// removeFromStringSlice returns s with the first occurrence of v removed (or s
// unchanged if v isn't present).
func removeFromStringSlice(s []string, v string) []string {
	for i, x := range s {
		if x == v {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

// doRetreat handles RETREAT: breaks off every melee engagement the player has —
// both what they've ADVANCEd on and what has ADVANCEd/aggro'd on them — so they're
// free to GO/CLIMB/portal/FLY again. See isEngagedInMelee for what counts as
// "engaged" and the blocking checks in movement.go/doFly that require this first.
func (e *GameEngine) doRetreat(ctx context.Context, player *Player) *CommandResult {
	if player.Dead {
		return &CommandResult{Messages: []string{"You can't do that. You are dead."}}
	}
	if player.Stunned {
		return &CommandResult{Messages: []string{"You are stunned and cannot retreat!"}}
	}
	if player.Position == 2 {
		return &CommandResult{Messages: []string{"You can't retreat while laying down! Stand up first."}}
	}
	if player.Immobilized {
		return &CommandResult{Messages: []string{"You are rooted to the spot and cannot retreat!"}}
	}
	if len(player.Entangles) > 0 {
		return &CommandResult{Messages: []string{fmt.Sprintf("You are bound by %s and cannot retreat!", player.Entangles[0].SpellName)}}
	}
	if player.IsImprisoned() {
		return &CommandResult{Messages: []string{"You are trapped within a blue force bubble and cannot retreat!"}}
	}
	if !e.isEngagedInMelee(player) {
		return &CommandResult{Messages: []string{"You aren't engaged with anyone."}}
	}

	e.breakAllMeleeEngagements(player)

	return &CommandResult{
		Messages:      []string{"You retreat from combat."},
		RoomBroadcast: []string{fmt.Sprintf("%s retreats from combat.", player.DisplayNameCap())},
	}
}

// breakAllMeleeEngagements clears every melee engagement a player has — both
// directions (what they've ADVANCEd on/been ADVANCEd on, any monster that has
// closed to melee/aggro'd on them) plus the older CombatTarget/Joined pair set by
// ATTACK — leaving isEngagedInMelee false. Shared by RETREAT and FLEE (see
// doFlee): fleeing that only cleared the single CombatTarget monster left every
// other monster that had aggro'd on the player (MonsterInstance.Target) still
// locked on, and left AdvancedOnMonsters/AdvancedOnPlayers/AdvancedByPlayers
// entirely untouched, so a "successful" flee didn't actually disengage anything
// the new ADVANCE/RETREAT system tracks.
func (e *GameEngine) breakAllMeleeEngagements(player *Player) {
	// Clear the player's own outgoing advances, removing them from each target's
	// reciprocal "advanced on me" bookkeeping (players only — monster instances
	// don't track an incoming list, see AdvancedOnMonsters's doc comment).
	player.AdvancedOnMonsters = nil
	for _, name := range player.AdvancedOnPlayers {
		if other := e.findOnlinePlayerByName(name); other != nil && other.RoomNumber == player.RoomNumber {
			other.AdvancedByPlayers = removeFromStringSlice(other.AdvancedByPlayers, player.FirstName)
		}
	}
	player.AdvancedOnPlayers = nil

	// Also break the reverse case: any monster that has closed to melee/aggro'd
	// on them, and any other player who has ADVANCEd on them.
	if e.monsterMgr != nil {
		e.monsterMgr.mu.Lock()
		for i := range e.monsterMgr.instances {
			inst := &e.monsterMgr.instances[i]
			if inst.RoomNumber == player.RoomNumber && inst.Target == player.FirstName {
				inst.Target = ""
			}
		}
		e.monsterMgr.mu.Unlock()
	}
	if e.sessions != nil {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.RoomNumber == player.RoomNumber {
				p.AdvancedOnPlayers = removeFromStringSlice(p.AdvancedOnPlayers, player.FirstName)
			}
		}
	}
	player.AdvancedByPlayers = nil

	e.disengageCombat(player)
}

// doBreakFree handles BREAK FREE: Combat Maneuvering's (skill 10) chance to
// escape magical webs/bindings tracked in player.Entangles (Plant Snare, and any
// future player-targeting Web/Tentacles — see Player.Entangles's doc comment).
// Per skills.txt: "The chance is only 2% per level of skill known, and incurs a
// five second round time in the attempt." Deliberately does not touch the
// Imprison spell or the older Immobilized flag (psi Immobilize/Imprisonment Rune
// traps) — skills.txt calls those "totally impenetrable," and Freedom (505),
// which shares the same Entangles-only scope, already draws that same line.
func (e *GameEngine) doBreakFree(ctx context.Context, player *Player) *CommandResult {
	if player.Dead {
		return &CommandResult{Messages: []string{"You can't do that. You are dead."}}
	}
	if len(player.Entangles) == 0 {
		return &CommandResult{Messages: []string{"You aren't bound by anything to break free from."}}
	}
	if player.RoundTimeExpiry.After(time.Now()) {
		remaining := int(player.RoundTimeExpiry.Sub(time.Now()).Seconds()) + 1
		return &CommandResult{Messages: []string{fmt.Sprintf("[Wait %d seconds...]", remaining)}}
	}
	cmSkill := player.Skills[10]
	if !player.IsGM && cmSkill < 1 {
		return &CommandResult{Messages: []string{"You have no training in Combat Maneuvering."}}
	}

	player.RoundTime = 5
	player.RoundTimeExpiry = time.Now().Add(5 * time.Second)

	chance := cmSkill * 2
	if chance > 100 {
		chance = 100
	}
	if rand.Intn(100) < chance {
		idx := rand.Intn(len(player.Entangles))
		removed := player.Entangles[idx]
		player.Entangles = append(player.Entangles[:idx], player.Entangles[idx+1:]...)
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You struggle and break free from %s! [Round: 5 sec]", removed.SpellName)},
			RoomBroadcast: []string{fmt.Sprintf("%s struggles and breaks free!", player.DisplayNameCap())},
			PlayerState:   player,
		}
	}
	return &CommandResult{
		Messages:      []string{"You struggle but fail to break free. [Round: 5 sec]"},
		RoomBroadcast: []string{fmt.Sprintf("%s struggles against their bonds.", player.DisplayNameCap())},
		PlayerState:   player,
	}
}
