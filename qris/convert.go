package qris

import (
	"strconv"

	"github.com/hirotomasato/paygateme/core"
)

// StaticToDynamicQris converts a static QRIS payload into a dynamic or hybrid one
// carrying a fixed amount. It injects tag 54 (transaction amount) and updates
// tag 01 (point of initiation).
//
// By default, it preserves POIStatic ("11") because ShopeePay rejects on-us
// POIDynamic ("12") as an expired internal order. POI "11" keeps tag 54 pre-filled
// and locked across all major Indonesian banks and e-wallets while passing ShopeePay
// scanner validation. Pass POIDynamic ("12") as optional third argument if strictly needed.
func StaticToDynamicQris(staticPayload string, amount int64, poi ...string) (string, error) {
	if amount <= 0 {
		return "", core.NewBaseError(core.CodeQRISParseError,
			"QRIS amount must be a positive integer")
	}

	// Verify the source checksum before trusting the payload.
	if !IsValidQRISChecksum(staticPayload) {
		return "", core.NewBaseError(core.CodeQRISParseError,
			"QRIS checksum is invalid; refusing to build a payable QR from it")
	}

	m, err := ParseEmv(staticPayload)
	if err != nil {
		return "", err
	}

	if _, ok := m[TagPayloadFormat]; !ok {
		return "", core.NewBaseError(core.CodeQRISParseError,
			"Input does not look like a QRIS payload (missing tag 00)")
	}

	targetPoi := POIStatic
	if len(poi) > 0 && poi[0] != "" {
		targetPoi = poi[0]
	}

	m[TagPointOfInitiation] = targetPoi
	m[TagTransactionAmount] = strconv.FormatInt(amount, 10)

	return BuildEmv(m)
}