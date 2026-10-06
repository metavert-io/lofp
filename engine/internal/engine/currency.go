package engine

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/jonradoff/lofp/internal/gameworld"
)

// Regional currencies (GMSCRIPT.DOC, "Ability to define new units of currency
// for new regions"):
//
//	MONEYDEF <x> <name> <exchange rate> <item#>
//
// defines money type x (1-9), native to region x: rooms in region x use it for
// their shops, bank and healer, and REGIONDEF x TREASURE <pct> is the chance a
// monster there carries it instead of the base gold/silver/copper. The exchange
// rate is how many units roughly equal one gold crown; item# is the MONEY item
// archetype (PARAMETER2 = x) for coins lying on the ground.
//
// Throughout this file a nil *MoneyDef means the base currency, whose amounts
// are always in copper (100 copper = 1 gold crown). A regional amount is a
// plain count of that currency's single denomination.
//
// Loaded from REGIONS.SCR/ADJNOUN.SCR: dollar (1), kragenmark (2, Kragenholm and
// the Deep Realms), danir (3, Shamradur), shard (4), darktar (5, Ilzar).

// loadMoneyDefs indexes MONEYDEFs by type number. A later definition replaces
// an earlier one (ADJNOUN.SCR defines darktar at 2/gold, then REGIONS.SCR at
// 100/gold — Ilzar's shop prices only make sense at 100).
func (e *GameEngine) loadMoneyDefs(defs []gameworld.MoneyDef) {
	e.moneyDefs = make(map[int]*gameworld.MoneyDef)
	for i := range defs {
		if defs[i].ID >= 1 && defs[i].ID <= 9 && defs[i].ExchangeRate > 0 {
			e.moneyDefs[defs[i].ID] = &defs[i]
		}
	}
}

// roomCurrency returns the currency used by a room's shops, bank and healer:
// its region's MONEYDEF for regions 1-9, else nil (base currency).
func (e *GameEngine) roomCurrency(roomNum int) *gameworld.MoneyDef {
	room := e.rooms[roomNum]
	if room == nil || room.Region < 1 || room.Region > 9 {
		return nil
	}
	return e.moneyDefs[room.Region]
}

// treasureCurrency picks the currency a monster killed/searched/robbed in
// roomNum carries: the region's currency with its REGIONDEF TREASURE percent
// chance, else base. A currency region with no TREASURE line uses its own
// currency every time (regions 1 and 3 have none; their shops only take local
// money, so base-currency drops there would be useless).
func (e *GameEngine) treasureCurrency(roomNum int) *gameworld.MoneyDef {
	md := e.roomCurrency(roomNum)
	if md == nil {
		return nil
	}
	pct := 100
	if reg := e.regions[md.ID]; reg != nil && reg.Treasure > 0 {
		pct = reg.Treasure
	}
	if rand.Intn(100) < pct {
		return md
	}
	return nil
}

// monsterCurrency is the currency monster instID (in roomNum) carries. The
// treasureCurrency roll is made once per monster and remembered on the
// instance, so its death drop, chest coins, SEARCH and ROB all pay in the same
// currency instead of each rolling the region's TREASURE percent separately.
func (e *GameEngine) monsterCurrency(instID, roomNum int) *gameworld.MoneyDef {
	if e.roomCurrency(roomNum) == nil || e.monsterMgr == nil {
		return e.treasureCurrency(roomNum)
	}
	e.monsterMgr.mu.Lock()
	defer e.monsterMgr.mu.Unlock()
	idx := e.monsterMgr.indexOfID(instID)
	if idx < 0 {
		return e.treasureCurrency(roomNum)
	}
	inst := &e.monsterMgr.instances[idx]
	if !inst.CoinTypeSet {
		inst.CoinTypeSet = true
		inst.CoinType = 0
		if md := e.treasureCurrency(roomNum); md != nil {
			inst.CoinType = md.ID
		}
	}
	return e.moneyDefs[inst.CoinType]
}

// currencyByName finds a regional currency by singular or plural name.
func (e *GameEngine) currencyByName(name string) *gameworld.MoneyDef {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, md := range e.moneyDefs {
		if name == md.Name || name == currencyPlural(md) {
			return md
		}
	}
	return nil
}

// currencyForItem returns the regional currency a MONEY item archetype stands
// for (its PARAMETER2), or nil for base coins/non-money.
func (e *GameEngine) currencyForItem(arch int) *gameworld.MoneyDef {
	def := e.items[arch]
	if def == nil || def.Type != "MONEY" || def.Parameter2 <= 0 {
		return nil
	}
	return e.moneyDefs[def.Parameter2]
}

func currencyPlural(md *gameworld.MoneyDef) string { return md.Name + "s" }

// formatCurrency renders an amount: formatPrice (copper) for base, else
// "1 kragenmark" / "12 kragenmarks".
func formatCurrency(md *gameworld.MoneyDef, amount int) string {
	if md == nil {
		return formatPrice(amount)
	}
	if amount == 1 {
		return "1 " + md.Name
	}
	return fmt.Sprintf("%d %s", amount, currencyPlural(md))
}

// copperToCurrency converts a base value in copper to md (unchanged for base).
// Uses the MONEYDEF exchange rate (units per gold crown), rounding to nearest,
// and never turns a non-zero value into 0.
func copperToCurrency(md *gameworld.MoneyDef, copper int) int {
	if md == nil || copper <= 0 {
		return copper
	}
	units := (copper*md.ExchangeRate + 50) / 100
	if units < 1 {
		units = 1
	}
	return units
}

// currencyToCopper converts an md amount back to its base value in copper.
func currencyToCopper(md *gameworld.MoneyDef, amount int) int {
	if md == nil {
		return amount
	}
	return amount * 100 / md.ExchangeRate
}

// playerFunds is what p carries in md (base: total copper value).
func playerFunds(p *Player, md *gameworld.MoneyDef) int {
	if md == nil {
		return p.Gold*100 + p.Silver*10 + p.Copper
	}
	return p.Currencies[md.ID]
}

// spendFunds takes amount of md from p, or returns false (taking nothing) if
// they can't cover it.
func (e *GameEngine) spendFunds(p *Player, md *gameworld.MoneyDef, amount int) bool {
	if playerFunds(p, md) < amount {
		return false
	}
	if md == nil {
		e.deductCopper(p, amount)
		return true
	}
	p.Currencies[md.ID] -= amount
	return true
}

// addFunds gives p amount of md (base: copper, normalized into gold/silver).
func addFunds(p *Player, md *gameworld.MoneyDef, amount int) {
	if amount <= 0 {
		return
	}
	if md == nil {
		p.Copper += amount
		p.Silver += p.Copper / 10
		p.Copper %= 10
		p.Gold += p.Silver / 10
		p.Silver %= 10
		return
	}
	if p.Currencies == nil {
		p.Currencies = map[int]int{}
	}
	p.Currencies[md.ID] += amount
}

// bankFunds / addBankFunds / spendBankFunds are the bank-account equivalents.
// Each bank holds one currency (GMSCRIPT.DOC: "Characters may have a bank
// account for each currency type").
func bankFunds(p *Player, md *gameworld.MoneyDef) int {
	if md == nil {
		return p.BankGold*100 + p.BankSilver*10 + p.BankCopper
	}
	return p.BankCurrencies[md.ID]
}

func addBankFunds(p *Player, md *gameworld.MoneyDef, amount int) {
	if md == nil {
		p.BankCopper += amount
		return
	}
	if p.BankCurrencies == nil {
		p.BankCurrencies = map[int]int{}
	}
	p.BankCurrencies[md.ID] += amount
}

func spendBankFunds(p *Player, md *gameworld.MoneyDef, amount int) bool {
	if bankFunds(p, md) < amount {
		return false
	}
	if md != nil {
		p.BankCurrencies[md.ID] -= amount
		return true
	}
	remaining := amount
	if p.BankCopper >= remaining {
		p.BankCopper -= remaining
		return true
	}
	remaining -= p.BankCopper
	p.BankCopper = 0
	sn := (remaining + 9) / 10
	if p.BankSilver >= sn {
		p.BankSilver -= sn
		p.BankCopper += sn*10 - remaining
		return true
	}
	remaining -= p.BankSilver * 10
	p.BankSilver = 0
	gn := (remaining + 99) / 100
	p.BankGold -= gn
	p.BankCopper += gn*100 - remaining
	return true
}

// regionalWealthLines lists the regional currencies p carries (or, bank=true,
// holds in the bank), in type order — e.g. ["12 kragenmarks", "3 shards"].
func (e *GameEngine) regionalWealthLines(p *Player, bank bool) []string {
	src := p.Currencies
	if bank {
		src = p.BankCurrencies
	}
	var ids []int
	for id, n := range src {
		if n > 0 && e.moneyDefs[id] != nil {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, formatCurrency(e.moneyDefs[id], src[id]))
	}
	return out
}

// newCoinPile builds a MONEY room item / container entry for a treasure drop
// worth copper in base terms, in md (nil = base: Archetype 0, Val1 copper).
func newCoinPileValue(md *gameworld.MoneyDef, copper int) (arch, val1 int) {
	if md == nil {
		return 0, copper
	}
	return md.ItemNum, copperToCurrency(md, copper)
}

// formatCoinPile describes a MONEY pile (State "MONEY") in a container or
// treasure message: "some coins (1 gold, 5 copper)", "some kragenmarks (12)".
func (e *GameEngine) formatCoinPile(arch, val1 int) string {
	if md := e.currencyForItem(arch); md != nil {
		return fmt.Sprintf("some %s (%d)", currencyPlural(md), val1)
	}
	switch arch {
	case coinArchGold:
		return fmt.Sprintf("some coins (%d gold)", val1)
	case coinArchSilver:
		return fmt.Sprintf("some coins (%d silver)", val1)
	}
	return formatCoinStr(val1)
}

// coinPileRoomName is how a MONEY pile shows in a room's item list.
func (e *GameEngine) coinPileRoomName(arch int) string {
	if md := e.currencyForItem(arch); md != nil {
		return "some " + currencyPlural(md)
	}
	return "some coins"
}

// migrateRegionalCoinItems folds any regional MONEY items a player carries
// (from before currencies were tracked as purse counters, e.g. a GM @additem)
// into Currencies. Returns true if anything changed.
func (e *GameEngine) migrateRegionalCoinItems(p *Player) bool {
	changed := false
	kept := p.Inventory[:0]
	for _, ii := range p.Inventory {
		if md := e.currencyForItem(ii.Archetype); md != nil {
			n := ii.Val1
			if n <= 0 {
				n = 1
			}
			addFunds(p, md, n)
			changed = true
			continue
		}
		kept = append(kept, ii)
	}
	p.Inventory = kept
	return changed
}

// wealthLines is WEALTH's (and COUNT MONEY's) output: one line for the base
// coins actually carried (zero denominations left out), then one line per
// regional currency carried — nothing for a currency the player has none of:
//
//	You have 17 gold crowns, 4 silver shillings and 4 copper pennies.
//	You have 425 kragenmarks.
func (e *GameEngine) wealthLines(p *Player) []string {
	var base []string
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	if p.Gold > 0 {
		base = append(base, plural(p.Gold, "gold crown", "gold crowns"))
	}
	if p.Silver > 0 {
		base = append(base, plural(p.Silver, "silver shilling", "silver shillings"))
	}
	if p.Copper > 0 {
		base = append(base, plural(p.Copper, "copper penny", "copper pennies"))
	}
	var lines []string
	if len(base) > 0 {
		list := base[0]
		if len(base) > 1 {
			list = strings.Join(base[:len(base)-1], ", ") + " and " + base[len(base)-1]
		}
		lines = append(lines, "You have "+list+".")
	}
	for _, amt := range e.regionalWealthLines(p, false) {
		lines = append(lines, "You have "+amt+".")
	}
	if len(lines) == 0 {
		lines = []string{"You have no money."}
	}
	return lines
}

// CurrencyAmounts maps each regional currency p carries (plural name) to its
// amount, for GMCP Char.Status.
func (e *GameEngine) CurrencyAmounts(p *Player) map[string]int {
	out := map[string]int{}
	for id, n := range p.Currencies {
		if md := e.moneyDefs[id]; md != nil && n > 0 {
			out[currencyPlural(md)] = n
		}
	}
	return out
}
