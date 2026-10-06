package engine

import (
	"context"
	"fmt"
	"math/rand"
)

// Falling out of the sky. A player whose flight ends (Fly spell expiring —
// including while logged out — psionic Flight/Levitate released, or Fly
// dispelled) while in an AERIAL room has nothing to stand on: before this, the
// flight-ending code just set Position 0, leaving them "standing" in the sky,
// unable to DESCEND (which requires Position 4) and with no exit down they
// could walk. Now they fall to the ground and take crushing damage.

// isAerialRoom reports whether roomNum is an AERIAL-terrain (open sky) room.
func (e *GameEngine) isAerialRoom(roomNum int) bool {
	r := e.rooms[roomNum]
	return r != nil && r.Terrain == "AERIAL"
}

// skyDownStep returns the room one step below an aerial room: its DROPLOC
// (SKYMAP.SCR sets it to the ground below on nearly every sky room), else its
// BELOW or D exit, else 0.
func (e *GameEngine) skyDownStep(roomNum int) int {
	r := e.rooms[roomNum]
	if r == nil {
		return 0
	}
	if r.DropLoc > 0 && e.rooms[r.DropLoc] != nil {
		return r.DropLoc
	}
	for _, key := range []string{"BELOW", "D"} {
		if d, ok := r.Exits[key]; ok && e.rooms[d] != nil {
			return d
		}
	}
	return 0
}

// skyLandingRoom finds where a player falling from aerial room start lands:
// straight down (following skyDownStep through any further sky rooms) if
// possible; otherwise from the nearest sky room, by lateral exits, that does
// have a way down (a few SKYMAP.SCR rooms have neither DROPLOC nor BELOW);
// failing that, the nearest non-aerial room bordering the sky. 0 if none.
func (e *GameEngine) skyLandingRoom(start int) int {
	fallFrom := func(n int) int {
		for i := 0; i < 10; i++ {
			d := e.skyDownStep(n)
			if d == 0 {
				return 0
			}
			if !e.isAerialRoom(d) {
				return d
			}
			n = d
		}
		return 0
	}
	queue := []int{start}
	seen := map[int]bool{start: true}
	border := 0
	for len(queue) > 0 && len(seen) < 1000 {
		n := queue[0]
		queue = queue[1:]
		if g := fallFrom(n); g > 0 {
			return g
		}
		for _, x := range e.rooms[n].Exits {
			if seen[x] || e.rooms[x] == nil {
				continue
			}
			seen[x] = true
			if e.isAerialRoom(x) {
				queue = append(queue, x)
			} else if border == 0 {
				border = x
			}
		}
	}
	return border
}

// fallIfFlightLost makes p fall to the ground if they're in an aerial room and
// can no longer fly (not Drakin, no active flight). It moves them, applies
// crushing damage (10-25% of max body points, never below 1 — losing a spell to
// a logout shouldn't be a death sentence) and returns the fall messages; nil if
// no fall happened. announce broadcasts the fall to both rooms and delivers the
// landing room's look/IFENTRY output to p (for mid-session falls); a login fall
// passes false, since EnterRoom shows the look and the login "arrives" echo
// covers the room.
func (e *GameEngine) fallIfFlightLost(ctx context.Context, p *Player, announce bool) []string {
	if p.IsFlying() || !e.isAerialRoom(p.RoomNumber) {
		return nil
	}
	dest := e.skyLandingRoom(p.RoomNumber)
	if dest == 0 {
		return nil
	}
	oldRoom := p.RoomNumber

	dmg := p.MaxBodyPoints * (10 + rand.Intn(16)) / 100
	if dmg < 3 {
		dmg = 3
	}
	if dmg >= p.BodyPoints {
		dmg = p.BodyPoints - 1
	}
	if dmg < 0 {
		dmg = 0
	}
	p.BodyPoints -= dmg

	e.breakAllMeleeEngagements(p)
	e.disengageCombat(p)
	p.RoomNumber = dest
	p.Position = 0
	e.SavePlayer(ctx, p)

	msgs := []string{
		"With nothing holding you aloft, you plummet toward the ground!",
		fmt.Sprintf("You slam into the ground! [%d crushing damage, %d/%d BP]", dmg, p.BodyPoints, p.MaxBodyPoints),
	}
	if !announce {
		return msgs
	}

	if !p.IsConcealed() {
		if e.roomBroadcastExclude != nil {
			e.roomBroadcastExclude(oldRoom, p.FirstName, []string{fmt.Sprintf("%s plummets toward the ground!", p.DisplayNameCap())})
			e.roomBroadcastExclude(dest, p.FirstName, []string{fmt.Sprintf("%s crashes to the ground from above!", p.DisplayNameCap())})
		}
	}
	msgs = append(msgs, e.doLook(p).Messages...)
	entry := &CommandResult{}
	if room := e.rooms[dest]; room != nil {
		e.applyEntryScripts(ctx, p, room, entry)
	}
	msgs = append(msgs, entry.Messages...)
	if len(entry.RoomBroadcast) > 0 && e.roomBroadcastExclude != nil {
		e.roomBroadcastExclude(dest, p.FirstName, entry.RoomBroadcast)
	}
	return msgs
}
