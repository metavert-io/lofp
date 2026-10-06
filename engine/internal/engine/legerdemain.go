package engine

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"strings"
	"time"
)

// Legerdemain (skill 21): "The subtle arts of sleight of hand, picking pockets and
// shoplifting ... The more levels of legerdemain a character has the more
// accurately he or she can determine the worth of an item." (LEGENDS.DOC)
//
// Commands (rules per the game owner's spec, 2026-09-24 — no original formulas
// survive):
//   - APPRAISE <item> away from a merchant: an estimate of the merchant sell
//     value, ±40/30/20/10% at levels 1-4 and exact from level 5.
//   - FILCH/STEAL <item>: pick an item up off the ground with no room echo.
//   - ROB/PICKPOCKET (shortest form PICKP; PICK is lockpicking)/STEAL
//     <player|creature>: pick a pocket for coins.
//   - ROB/STEAL with no target in a store: shoplift from the till.
// Robbing is limited to one attempt per target (player, creature or store) per
// game day.
const legerdemainSkill = 21

// legerdemainChance is the % chance a FILCH or ROB attempt succeeds: 25 base,
// +Agility/15, +Quickness/15, +5 per Legerdemain level, +10 at night, +50 in a
// dark room with no light source, +25 while Invisible/Phantom Form, and
// -Perception/10 of a player target (victim may be nil). Capped at
// legerdemainMaxChance, like combat and spell rolls.
func (e *GameEngine) legerdemainChance(player, victim *Player) int {
	chance := 25 + player.Agility/15 + player.Quickness/15 + 5*player.Skills[legerdemainSkill]
	if IsNight() {
		chance += 10
	}
	if e.roomIsUnlitDark(player.RoomNumber) {
		chance += 50
	}
	if player.Invisible || player.PhantomForm {
		chance += 25
	}
	if victim != nil {
		chance -= victim.Perception / 10
	}
	return min(chance, legerdemainMaxChance)
}

const legerdemainMaxChance = 95

// roomIsUnlitDark reports whether a room's script lighting is DARKNESS
// (GMSCRIPT.DOC) and nothing in it is giving off light — no LIT item carried
// by anyone present, or lying in the room.
func (e *GameEngine) roomIsUnlitDark(roomNum int) bool {
	room := e.rooms[roomNum]
	if room == nil || room.Lighting != "DARKNESS" {
		return false
	}
	for _, ri := range room.Items {
		if ri.State == "LIT" {
			return false
		}
	}
	if e.sessions != nil {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.RoomNumber != roomNum {
				continue
			}
			if p.Wielded != nil && p.Wielded.State == "LIT" || p.OffHand != nil && p.OffHand.State == "LIT" {
				return false
			}
			for _, ii := range p.Inventory {
				if ii.State == "LIT" {
					return false
				}
			}
			for _, ii := range p.Worn {
				if ii.State == "LIT" {
					return false
				}
			}
		}
	}
	return true
}

// robGameDay is the absolute game-day number (GameDay wraps yearly, this doesn't).
func robGameDay() int { return GameMinutes() / (60 * 24) }

// robAttemptUsed reports whether player already tried to rob key today, and if
// not, records this attempt (pruning entries from earlier days).
func robAttemptUsed(player *Player, key string) bool {
	today := robGameDay()
	if player.RobAttempts[key] == today {
		return true
	}
	for k, day := range player.RobAttempts {
		if day != today {
			delete(player.RobAttempts, k)
		}
	}
	if player.RobAttempts == nil {
		player.RobAttempts = map[string]int{}
	}
	player.RobAttempts[key] = today
	return false
}

const robAlreadyTriedMsg = "You've already attempted to rob that target all you can for today."

// robCapCopper is the most a rob of a player or store can yield: one gold crown
// per Legerdemain level, scaled by a random 50-100%.
func robCapCopper(level int) int {
	maxCopper := level * 100
	return maxCopper/2 + rand.Intn(maxCopper/2+1)
}

// doFilch handles FILCH/STEAL <item>: GET a floor item with no echo on success.
// A failed roll still picks the item up, but with the normal GET echo —
// anonymized to "Something picks up ..." while Invisible/Phantom Form (other
// concealment, e.g. Hidden, already suppresses the echo entirely).
func (e *GameEngine) doFilch(ctx context.Context, player *Player, args []string) *CommandResult {
	if msg := formActionBlockMessage(player); msg != "" {
		return &CommandResult{Messages: []string{msg}}
	}
	if player.Skills[legerdemainSkill] < 1 && !player.IsGM {
		return &CommandResult{Messages: []string{"You have no training in Legerdemain."}}
	}
	if busy := legerdemainBusy(player); busy != nil {
		return busy
	}
	ok, rollLine := e.legerdemainRoll(player, nil)
	echo := pickupEchoSilent
	if !ok {
		echo = pickupEchoNormal
		if (player.Invisible || player.PhantomForm) && !player.Hidden && !player.GMHidden && !player.GMInvis {
			echo = pickupEchoSomething
		}
	}
	var outcome getOutcome
	res := e.doGetFloor(ctx, player, "GET", args, echo, &outcome)
	if !outcome.PickedUp {
		return res // nothing taken (not found, blocked, immovable) — no roll shown, no round time
	}
	msgs := []string{fmt.Sprintf("You get in position to filch %s.", outcome.Name), rollLine}
	msgs = append(msgs, res.Messages...)
	if ok && len(msgs) > 2 {
		// Replace doGetFloor's "You pick up X." (always its last line).
		msgs[len(msgs)-1] = "You secretly take " + outcome.Taken
	}
	res.Messages = append(msgs, startLegerdemainRoundTime(player))
	return res
}

// legerdemainRoundTime is the round time FILCH/STEAL/ROB incur (halved by
// Haste via applyRoundTime).
const legerdemainRoundTime = 5

// legerdemainBusy returns a "[Wait N seconds...]" result while the player is
// still in round time, or nil.
func legerdemainBusy(player *Player) *CommandResult {
	if player.RoundTimeExpiry.After(time.Now()) {
		remaining := int(time.Until(player.RoundTimeExpiry).Seconds()) + 1
		return &CommandResult{Messages: []string{fmt.Sprintf("[Wait %d seconds...]", remaining)}}
	}
	return nil
}

// legerdemainRoll rolls 1-100 against legerdemainChance (success on roll <=
// chance) and returns the outcome plus the "[Success: N%, Roll: R] Success!"
// line shown to the player.
func (e *GameEngine) legerdemainRoll(player, victim *Player) (bool, string) {
	chance := max(e.legerdemainChance(player, victim), 0)
	roll := rand.Intn(100) + 1
	ok := roll <= chance
	verdict := "Failure!"
	if ok {
		verdict = "Success!"
	}
	return ok, fmt.Sprintf("[Success: %d%%, Roll: %d] %s", chance, roll, verdict)
}

// startLegerdemainRoundTime applies legerdemainRoundTime and returns the
// " [Round: N sec]" line.
func startLegerdemainRoundTime(player *Player) string {
	rt := applyRoundTime(player, legerdemainRoundTime)
	player.RoundTime = rt
	player.RoundTimeExpiry = time.Now().Add(time.Duration(rt) * time.Second)
	return fmt.Sprintf(" [Round: %d sec]", rt)
}

// doRob handles ROB/PICKPOCKET <player|creature> and, with no target, robbing
// the store the player is standing in.
func (e *GameEngine) doRob(ctx context.Context, player *Player, args []string) *CommandResult {
	if msg := formActionBlockMessage(player); msg != "" {
		return &CommandResult{Messages: []string{msg}}
	}
	level := player.Skills[legerdemainSkill]
	if level < 1 && !player.IsGM {
		return &CommandResult{Messages: []string{"You have no training in Legerdemain."}}
	}
	if level < 1 {
		level = 1 // GM testing without the skill
	}
	if busy := legerdemainBusy(player); busy != nil {
		return busy
	}
	if len(args) == 0 {
		return e.doRobStore(ctx, player, level)
	}
	target := strings.ToLower(strings.Join(args, " "))

	if inst, def := e.findMonsterInRoom(player, target); inst != nil && def != nil {
		name := FormatMonsterName(def, e.monAdjs)
		article := articleFor(name, def.Unique)
		if def.Treasure <= 0 || inst.IsSummoned {
			return &CommandResult{Messages: []string{fmt.Sprintf("%s%s has nothing to rob.", capArticle(article), name)}}
		}
		if robAttemptUsed(player, fmt.Sprintf("M:%d", inst.ID)) {
			return &CommandResult{Messages: []string{robAlreadyTriedMsg}}
		}
		ok, rollLine := e.legerdemainRoll(player, nil)
		msgs := []string{fmt.Sprintf("You get in position to rob %s%s.", article, name), rollLine}
		if !ok {
			msgs = append(msgs, fmt.Sprintf("You fail to lift anything from %s%s.", article, name), startLegerdemainRoundTime(player))
			e.SavePlayer(ctx, player)
			return &CommandResult{Messages: msgs}
		}
		// Half of what the creature's TREASURE would drop as coins on death
		// (generateTreasure: TREASURE*5 + 0..TREASURE*5 copper).
		base := def.Treasure * 5
		coins := (base + rand.Intn(base+1)) / 2
		if coins < 1 {
			coins = 1
		}
		// A creature in a currency region carries what it would drop (currency.go).
		md := e.monsterCurrency(inst.ID, player.RoomNumber)
		amount := copperToCurrency(md, coins)
		addFunds(player, md, amount)
		msgs = append(msgs, fmt.Sprintf("You secretly take %s from %s%s.", formatCurrency(md, amount), article, name), awardRobXP(player, level), startLegerdemainRoundTime(player))
		e.SavePlayer(ctx, player)
		return &CommandResult{Messages: msgs}
	}

	if victim := e.findPlayerInRoom(player, target); victim != nil {
		if robAttemptUsed(player, "P:"+victim.FirstName) {
			return &CommandResult{Messages: []string{robAlreadyTriedMsg}}
		}
		ok, rollLine := e.legerdemainRoll(player, victim)
		msgs := []string{fmt.Sprintf("You get in position to rob %s.", victim.DisplayName()), rollLine}
		if !ok {
			// The victim feels it but isn't told who.
			if e.sendToPlayer != nil {
				e.sendToPlayer(victim.FirstName, []string{"You feel a tug on your coin purse!"})
			}
			msgs = append(msgs, fmt.Sprintf("You fail to lift anything from %s.", victim.DisplayName()), startLegerdemainRoundTime(player))
			e.SavePlayer(ctx, player)
			return &CommandResult{Messages: msgs}
		}
		purse := victim.Gold*100 + victim.Silver*10 + victim.Copper
		coins := min(robCapCopper(level), purse)
		if coins <= 0 {
			msgs = append(msgs, fmt.Sprintf("You slip a hand into %s's coin purse, but it's empty.", victim.DisplayName()), startLegerdemainRoundTime(player))
			e.SavePlayer(ctx, player)
			return &CommandResult{Messages: msgs}
		}
		e.deductCopper(victim, coins)
		e.SavePlayer(ctx, victim)
		e.addCoinsToPlayer(player, 0, coins)
		msgs = append(msgs, fmt.Sprintf("You secretly take %s from %s's coin purse.", formatPrice(coins), victim.DisplayName()), awardRobXP(player, level), startLegerdemainRoundTime(player))
		e.SavePlayer(ctx, player)
		return &CommandResult{Messages: msgs}
	}

	return &CommandResult{Messages: []string{fmt.Sprintf("You don't see '%s' here.", strings.Join(args, " "))}}
}

// awardRobXP grants the experience for a successful rob that took gold (from a
// player, creature or store): 50 per Legerdemain level, randomized by ±10%.
// Like crafting XP, it's added directly; build points and levels catch up via
// recalcBuildPoints at the next kill or EXP. Returns the award message.
func awardRobXP(player *Player, level int) string {
	base := 50 * level
	xp := base - base/10 + rand.Intn(base/5+1)
	player.Experience += xp
	return fmt.Sprintf("You have been awarded %d experience points.", xp)
}

// doRobStore handles ROB/STEAL with no target in a room that sells things
// (room.StoreItems): up to one gold crown per Legerdemain level from the till.
func (e *GameEngine) doRobStore(ctx context.Context, player *Player, level int) *CommandResult {
	room := e.rooms[player.RoomNumber]
	if room == nil || len(room.StoreItems) == 0 {
		return &CommandResult{Messages: []string{"Rob whom?"}}
	}
	if robAttemptUsed(player, fmt.Sprintf("S:%d", room.Number)) {
		return &CommandResult{Messages: []string{robAlreadyTriedMsg}}
	}
	ok, rollLine := e.legerdemainRoll(player, nil)
	msgs := []string{"You get in position to rob the till.", rollLine}
	if !ok {
		msgs = append(msgs, "You can't find a moment when the merchant isn't watching the till.", startLegerdemainRoundTime(player))
		e.SavePlayer(ctx, player)
		return &CommandResult{Messages: msgs}
	}
	// The till holds the local currency in a currency region (currency.go):
	// the same up-to-1-gold-crown-per-level value, converted.
	md := e.roomCurrency(player.RoomNumber)
	amount := copperToCurrency(md, robCapCopper(level))
	addFunds(player, md, amount)
	msgs = append(msgs, fmt.Sprintf("You secretly take %s from the till.", formatCurrency(md, amount)), awardRobXP(player, level), startLegerdemainRoundTime(player))
	e.SavePlayer(ctx, player)
	return &CommandResult{Messages: msgs}
}

// appraiseEstimate returns a Legerdemain appraiser's estimate of trueValue:
// within ±40/30/20/10% at levels 1-4, exact from level 5. The error is derived
// from a hash of the appraiser, the item and the skill level rather than rolled
// fresh, so re-appraising the same item gives the same answer instead of letting
// a player average many guesses into the true price.
func appraiseEstimate(player *Player, ii *InventoryItem, trueValue int) int {
	level := player.Skills[legerdemainSkill]
	spread := 50 - 10*level // percent: level 1 = 40, level 4 = 10
	if spread <= 0 || trueValue <= 0 {
		return trueValue
	}
	h := fnv.New32a()
	fmt.Fprintf(h, "%s|%d|%d|%d|%d|%d|%d|%d", player.FirstName, ii.Archetype, ii.Adj1, ii.Adj2, ii.Adj3, ii.Val1, ii.CraftedValue, level)
	pct := int(h.Sum32()%uint32(2*spread+1)) - spread // -spread..+spread
	est := trueValue + trueValue*pct/100
	if est < 1 {
		est = 1
	}
	return est
}

// doLegerdemainAppraise handles APPRAISE away from a merchant for a player
// with Legerdemain: an estimate (appraiseEstimate) of what a merchant would pay
// for a carried, wielded or worn item. Returns nil if nothing matches.
func (e *GameEngine) doLegerdemainAppraise(ctx context.Context, player *Player, target string, skip int) *CommandResult {
	candidates := make([]*InventoryItem, 0, len(player.Inventory)+len(player.Worn)+2)
	for i := range player.Inventory {
		candidates = append(candidates, &player.Inventory[i])
	}
	if player.Wielded != nil {
		candidates = append(candidates, player.Wielded)
	}
	if player.OffHand != nil {
		candidates = append(candidates, player.OffHand)
	}
	for i := range player.Worn {
		candidates = append(candidates, &player.Worn[i])
	}
	for _, ii := range candidates {
		itemDef := e.items[ii.Archetype]
		if itemDef == nil {
			continue
		}
		name := e.getItemNounName(itemDef)
		if !matchesTarget(name, target, e.getAdjName(ii.Adj1), e.getAdjName(ii.Adj2), e.getAdjName(ii.Adj3)) {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		displayName := e.formatItemName(itemDef, ii.Adj1, ii.Adj2, ii.Adj3, ii.Tail)
		hadValue := ii.Val1 > 0
		value := e.computeSellValue(itemDef, ii)
		if !hadValue && ii.Val1 > 0 {
			e.SavePlayer(ctx, player) // lock in a freshly-rolled gem value
		}
		// Quoted in what a merchant here would pay with (currency.go).
		md := e.roomCurrency(player.RoomNumber)
		if player.Skills[legerdemainSkill] >= 5 {
			return &CommandResult{Messages: []string{fmt.Sprintf("You study %s and judge that a merchant would pay %s for it.", displayName, formatCurrency(md, copperToCurrency(md, value)))}}
		}
		return &CommandResult{Messages: []string{fmt.Sprintf("You study %s and estimate that a merchant would pay about %s for it.", displayName, formatCurrency(md, copperToCurrency(md, appraiseEstimate(player, ii, value))))}}
	}
	return nil
}
