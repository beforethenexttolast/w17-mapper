// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package util

import (
	"math"
)

func MapRange(ix RawValue, ixMin RawValue, ixMax RawValue, iyMin RawValue, iyMax RawValue) RawValue {

	//use floats to avoid overflows
	fx := float64(ix)
	fxMin := float64(ixMin)
	fxMax := float64(ixMax)
	fyMin := float64(iyMin)
	fyMax := float64(iyMax)

	fy := fyMin + (fyMax-fyMin)*(fx-fxMin)/(fxMax-fxMin)
	fy = math.Max(fy, fyMin)
	fy = math.Min(fy, fyMax)

	return RawValue(fy)
}

type ChannelNumber int32

type RawValue int32
type CRSFValue uint32

const MaxRaw = RawValue(math.MaxInt16)
const MinRaw = RawValue(math.MinInt16)
const ZeroRaw = RawValue(0)
const DefaultTruthyRawValue = MaxRaw

const DefaultFalsyRawValue = ZeroRaw

const CRSFMinValue = 0
const CRSFMaxValue = 1984

// CRSFCenterValue is the CRSF stick-center anchor (1500us). It is the neutral a
// channel is driven to when its input cannot be evaluated -- see
// InputChannel.FailsafeValue.
//
// W17 fork addition. Do not use CRSFMinValue (0) as a neutral: 0 is below the
// nominal 172..1811 CRSF raw range, and a receiver that normalizes against
// those anchors reads it as FULL NEGATIVE deflection, not as center.
const CRSFCenterValue = 992
