package relayroom

import (
	"time"

	"golang.org/x/time/rate"
)

const (
	HardMaxOpenRooms      = 256
	HardMaxRoomRecords    = 4096
	HardMaxRoomCapacity   = 16
	HardMaxActiveSessions = 4096
	HardMaxRoomTTL        = 2 * time.Hour
	HardMaxGrantTTL       = 2 * time.Hour
	HardMaxSweepInterval  = time.Second
	HardMaxEmptyGrace     = 5 * time.Second
	HardMaxTombstoneTTL   = 60 * time.Second
	HardMaxChallengeTTL   = 3 * time.Second
	HardMaxBindingTTL     = 60 * time.Second
	HardMaxPreauthSources = 4096

	HardMaxPreauthSourcePacketRate        rate.Limit = 16
	HardMaxPreauthSourcePacketBurst                  = 160
	HardMaxPreauthSourceByteRate          rate.Limit = 19_200
	HardMaxPreauthSourceByteBurst                    = 192_000
	HardMaxPreauthGlobalPacketRate        rate.Limit = 128
	HardMaxPreauthGlobalPacketBurst                  = 1_280
	HardMaxPreauthGlobalByteRate          rate.Limit = 153_600
	HardMaxPreauthGlobalByteBurst                    = 1_536_000
	HardMaxSessionPacketRate                         = rate.Limit(40)
	HardMaxSessionPacketBurst                        = 40
	HardMaxSessionByteRate                           = rate.Limit(20_480)
	HardMaxSessionByteBurst                          = 20_480
	HardMaxRoomPacketRate                            = rate.Limit(160)
	HardMaxRoomPacketBurst                           = 160
	HardMaxRoomByteRate                              = rate.Limit(81_920)
	HardMaxRoomByteBurst                             = 81_920
	HardMaxAuthenticatedGlobalPacketRate             = rate.Limit(1_280)
	HardMaxAuthenticatedGlobalPacketBurst            = 1_280
	HardMaxAuthenticatedGlobalByteRate               = rate.Limit(655_360)
	HardMaxAuthenticatedGlobalByteBurst              = 655_360
	HardMaxRoomFanoutWriteRate                       = rate.Limit(480)
	HardMaxRoomFanoutWriteBurst                      = 480
	HardMaxRoomFanoutByteRate                        = rate.Limit(245_760)
	HardMaxRoomFanoutByteBurst                       = 245_760
	HardMaxGlobalFanoutWriteRate                     = rate.Limit(3_840)
	HardMaxGlobalFanoutWriteBurst                    = 3_840
	HardMaxGlobalFanoutByteRate                      = rate.Limit(1_966_080)
	HardMaxGlobalFanoutByteBurst                     = 1_966_080
)

type Limits struct {
	MaxOpenRooms      int
	MaxRoomRecords    int
	MaxRoomCapacity   int
	MaxActiveSessions int
	MaxRoomTTL        time.Duration
	MaxGrantTTL       time.Duration
	SweepInterval     time.Duration
	EmptyGrace        time.Duration
	TombstoneTTL      time.Duration
	ChallengeTTL      time.Duration
	BindingTTL        time.Duration

	PreauthSourcePacketRate  rate.Limit
	PreauthSourcePacketBurst int
	PreauthSourceByteRate    rate.Limit
	PreauthSourceByteBurst   int
	PreauthGlobalPacketRate  rate.Limit
	PreauthGlobalPacketBurst int
	PreauthGlobalByteRate    rate.Limit
	PreauthGlobalByteBurst   int

	SessionPacketRate              rate.Limit
	SessionPacketBurst             int
	SessionByteRate                rate.Limit
	SessionByteBurst               int
	RoomPacketRate                 rate.Limit
	RoomPacketBurst                int
	RoomByteRate                   rate.Limit
	RoomByteBurst                  int
	AuthenticatedGlobalPacketRate  rate.Limit
	AuthenticatedGlobalPacketBurst int
	AuthenticatedGlobalByteRate    rate.Limit
	AuthenticatedGlobalByteBurst   int
	RoomFanoutWriteRate            rate.Limit
	RoomFanoutWriteBurst           int
	RoomFanoutByteRate             rate.Limit
	RoomFanoutByteBurst            int
	GlobalFanoutWriteRate          rate.Limit
	GlobalFanoutWriteBurst         int
	GlobalFanoutByteRate           rate.Limit
	GlobalFanoutByteBurst          int
}

func DefaultLimits() Limits {
	return Limits{
		MaxOpenRooms:                   HardMaxOpenRooms,
		MaxRoomRecords:                 HardMaxRoomRecords,
		MaxRoomCapacity:                HardMaxRoomCapacity,
		MaxActiveSessions:              HardMaxActiveSessions,
		MaxRoomTTL:                     HardMaxRoomTTL,
		MaxGrantTTL:                    HardMaxGrantTTL,
		SweepInterval:                  HardMaxSweepInterval,
		EmptyGrace:                     HardMaxEmptyGrace,
		TombstoneTTL:                   HardMaxTombstoneTTL,
		ChallengeTTL:                   HardMaxChallengeTTL,
		BindingTTL:                     HardMaxBindingTTL,
		PreauthSourcePacketRate:        HardMaxPreauthSourcePacketRate,
		PreauthSourcePacketBurst:       HardMaxPreauthSourcePacketBurst,
		PreauthSourceByteRate:          HardMaxPreauthSourceByteRate,
		PreauthSourceByteBurst:         HardMaxPreauthSourceByteBurst,
		PreauthGlobalPacketRate:        HardMaxPreauthGlobalPacketRate,
		PreauthGlobalPacketBurst:       HardMaxPreauthGlobalPacketBurst,
		PreauthGlobalByteRate:          HardMaxPreauthGlobalByteRate,
		PreauthGlobalByteBurst:         HardMaxPreauthGlobalByteBurst,
		SessionPacketRate:              HardMaxSessionPacketRate,
		SessionPacketBurst:             HardMaxSessionPacketBurst,
		SessionByteRate:                HardMaxSessionByteRate,
		SessionByteBurst:               HardMaxSessionByteBurst,
		RoomPacketRate:                 HardMaxRoomPacketRate,
		RoomPacketBurst:                HardMaxRoomPacketBurst,
		RoomByteRate:                   HardMaxRoomByteRate,
		RoomByteBurst:                  HardMaxRoomByteBurst,
		AuthenticatedGlobalPacketRate:  HardMaxAuthenticatedGlobalPacketRate,
		AuthenticatedGlobalPacketBurst: HardMaxAuthenticatedGlobalPacketBurst,
		AuthenticatedGlobalByteRate:    HardMaxAuthenticatedGlobalByteRate,
		AuthenticatedGlobalByteBurst:   HardMaxAuthenticatedGlobalByteBurst,
		RoomFanoutWriteRate:            HardMaxRoomFanoutWriteRate,
		RoomFanoutWriteBurst:           HardMaxRoomFanoutWriteBurst,
		RoomFanoutByteRate:             HardMaxRoomFanoutByteRate,
		RoomFanoutByteBurst:            HardMaxRoomFanoutByteBurst,
		GlobalFanoutWriteRate:          HardMaxGlobalFanoutWriteRate,
		GlobalFanoutWriteBurst:         HardMaxGlobalFanoutWriteBurst,
		GlobalFanoutByteRate:           HardMaxGlobalFanoutByteRate,
		GlobalFanoutByteBurst:          HardMaxGlobalFanoutByteBurst,
	}
}

func validLimits(limits Limits) bool {
	return limits.MaxOpenRooms > 0 && limits.MaxOpenRooms <= HardMaxOpenRooms &&
		limits.MaxRoomRecords > 0 && limits.MaxRoomRecords <= HardMaxRoomRecords &&
		limits.MaxRoomCapacity > 0 && limits.MaxRoomCapacity <= HardMaxRoomCapacity &&
		limits.MaxActiveSessions > 0 && limits.MaxActiveSessions <= HardMaxActiveSessions &&
		limits.MaxRoomTTL > 0 && limits.MaxRoomTTL <= HardMaxRoomTTL &&
		limits.MaxGrantTTL > 0 && limits.MaxGrantTTL <= HardMaxGrantTTL &&
		limits.SweepInterval > 0 && limits.SweepInterval <= HardMaxSweepInterval &&
		limits.EmptyGrace > 0 && limits.EmptyGrace <= HardMaxEmptyGrace &&
		limits.TombstoneTTL > 0 && limits.TombstoneTTL <= HardMaxTombstoneTTL &&
		limits.ChallengeTTL > 0 && limits.ChallengeTTL <= HardMaxChallengeTTL &&
		limits.BindingTTL > 0 && limits.BindingTTL <= HardMaxBindingTTL &&
		validRate(limits.PreauthSourcePacketRate, HardMaxPreauthSourcePacketRate) &&
		limits.PreauthSourcePacketBurst > 0 && limits.PreauthSourcePacketBurst <= HardMaxPreauthSourcePacketBurst &&
		validRate(limits.PreauthSourceByteRate, HardMaxPreauthSourceByteRate) &&
		limits.PreauthSourceByteBurst > 0 && limits.PreauthSourceByteBurst <= HardMaxPreauthSourceByteBurst &&
		validRate(limits.PreauthGlobalPacketRate, HardMaxPreauthGlobalPacketRate) &&
		limits.PreauthGlobalPacketBurst > 0 && limits.PreauthGlobalPacketBurst <= HardMaxPreauthGlobalPacketBurst &&
		validRate(limits.PreauthGlobalByteRate, HardMaxPreauthGlobalByteRate) &&
		limits.PreauthGlobalByteBurst > 0 && limits.PreauthGlobalByteBurst <= HardMaxPreauthGlobalByteBurst &&
		validRate(limits.SessionPacketRate, HardMaxSessionPacketRate) &&
		limits.SessionPacketBurst > 0 && limits.SessionPacketBurst <= HardMaxSessionPacketBurst &&
		validRate(limits.SessionByteRate, HardMaxSessionByteRate) &&
		limits.SessionByteBurst > 0 && limits.SessionByteBurst <= HardMaxSessionByteBurst &&
		validRate(limits.RoomPacketRate, HardMaxRoomPacketRate) &&
		limits.RoomPacketBurst > 0 && limits.RoomPacketBurst <= HardMaxRoomPacketBurst &&
		validRate(limits.RoomByteRate, HardMaxRoomByteRate) &&
		limits.RoomByteBurst > 0 && limits.RoomByteBurst <= HardMaxRoomByteBurst &&
		validRate(limits.AuthenticatedGlobalPacketRate, HardMaxAuthenticatedGlobalPacketRate) &&
		limits.AuthenticatedGlobalPacketBurst > 0 && limits.AuthenticatedGlobalPacketBurst <= HardMaxAuthenticatedGlobalPacketBurst &&
		validRate(limits.AuthenticatedGlobalByteRate, HardMaxAuthenticatedGlobalByteRate) &&
		limits.AuthenticatedGlobalByteBurst > 0 && limits.AuthenticatedGlobalByteBurst <= HardMaxAuthenticatedGlobalByteBurst &&
		validRate(limits.RoomFanoutWriteRate, HardMaxRoomFanoutWriteRate) &&
		limits.RoomFanoutWriteBurst > 0 && limits.RoomFanoutWriteBurst <= HardMaxRoomFanoutWriteBurst &&
		validRate(limits.RoomFanoutByteRate, HardMaxRoomFanoutByteRate) &&
		limits.RoomFanoutByteBurst > 0 && limits.RoomFanoutByteBurst <= HardMaxRoomFanoutByteBurst &&
		validRate(limits.GlobalFanoutWriteRate, HardMaxGlobalFanoutWriteRate) &&
		limits.GlobalFanoutWriteBurst > 0 && limits.GlobalFanoutWriteBurst <= HardMaxGlobalFanoutWriteBurst &&
		validRate(limits.GlobalFanoutByteRate, HardMaxGlobalFanoutByteRate) &&
		limits.GlobalFanoutByteBurst > 0 && limits.GlobalFanoutByteBurst <= HardMaxGlobalFanoutByteBurst &&
		limits.MaxOpenRooms <= limits.MaxRoomRecords &&
		limits.MaxRoomCapacity <= limits.MaxActiveSessions &&
		limits.MaxGrantTTL <= limits.MaxRoomTTL
}

func validRate(value, maximum rate.Limit) bool {
	return value > 0 && value <= maximum
}
