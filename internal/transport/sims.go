package transport

import "unicode"

// SIM is one SIM the phone reports for sending. It keeps only the slot, the
// carrier name and the last four digits of the number, never the whole number.
type SIM struct {
	Slot         int32  `json:"slot"`
	Carrier      string `json:"carrier,omitempty"`
	NumberEnding string `json:"number_ending,omitempty"`
}

// SIMReporter reports the SIMs the phone last said it has. ok is false until
// the phone has sent its settings since the service connected.
type SIMReporter interface {
	SIMs() (sims []SIM, ok bool)
}

func (g *Google) SIMs() ([]SIM, bool) {
	g.mu.Lock()
	settings := g.settings
	g.mu.Unlock()
	if settings == nil {
		return nil, false
	}
	sims := []SIM{}
	for _, card := range settings.GetSIMCards() {
		data := card.GetSIMData()
		number := data.GetFormattedPhoneNumber()
		if number == "" {
			number = data.GetInternationalPhoneNumber()
		}
		sims = append(sims, SIM{Slot: data.GetSIMPayload().GetSIMNumber(), Carrier: data.GetCarrierName(), NumberEnding: lastDigits(number, 4)})
	}
	return sims, true
}

func (f *Fixture) SIMs() ([]SIM, bool) {
	return []SIM{{Slot: 1, Carrier: "Demo carrier", NumberEnding: "0100"}}, true
}

// lastDigits is the last n digits of s, or "" when it has fewer.
func lastDigits(s string, n int) string {
	digits := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits = append(digits, r)
		}
	}
	if len(digits) < n {
		return ""
	}
	return string(digits[len(digits)-n:])
}
