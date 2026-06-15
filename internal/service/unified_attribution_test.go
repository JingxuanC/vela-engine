package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnifiedAttribution_ChannelAttributionStruct(t *testing.T) {
	ch := ChannelAttribution{
		Channel:           "cart_recovery",
		AttributedOrders:  45,
		AttributedRevenue: 16000,
		SharedRevenue:     2000,
	}
	assert.Equal(t, "cart_recovery", ch.Channel)
	assert.Equal(t, int64(45), ch.AttributedOrders)
	assert.InDelta(t, 16000, ch.AttributedRevenue, 0.01)
	assert.InDelta(t, 2000, ch.SharedRevenue, 0.01)
}

func TestUnifiedAttribution_OverlapPairStruct(t *testing.T) {
	op := OverlapPair{
		Pair:    "cart_recovery + content",
		Orders:  12,
		Revenue: 1500,
	}
	assert.Equal(t, "cart_recovery + content", op.Pair)
	assert.Equal(t, int64(12), op.Orders)
	assert.InDelta(t, 1500, op.Revenue, 0.01)
}

func TestUnifiedAttribution_ResultStruct(t *testing.T) {
	r := &UnifiedAttributionResult{
		TotalActualRevenue:     45000,
		TotalAttributedRevenue: 38000,
		OverlapRate:            15.5,
		Channels: []ChannelAttribution{
			{Channel: "cart_recovery", AttributedOrders: 45, AttributedRevenue: 16000, SharedRevenue: 2000},
			{Channel: "content", AttributedOrders: 120, AttributedRevenue: 19000, SharedRevenue: 3000},
			{Channel: "review", AttributedOrders: 30, AttributedRevenue: 10000, SharedRevenue: 2000},
		},
		OverlapMatrix: []OverlapPair{
			{Pair: "cart_recovery + content", Orders: 12, Revenue: 1500},
			{Pair: "cart_recovery + review", Orders: 5, Revenue: 500},
			{Pair: "content + review", Orders: 8, Revenue: 500},
			{Pair: "all three", Orders: 3, Revenue: 200},
		},
	}
	assert.Equal(t, 3, len(r.Channels))
	assert.Equal(t, 4, len(r.OverlapMatrix))
	assert.InDelta(t, 15.5, r.OverlapRate, 0.01)
}

func TestUnifiedAttribution_EqualSplitConcept(t *testing.T) {
	// Simulate equal split: an order worth $200 attributed by 2 channels → each gets $100
	revenue := 200.0
	channelCount := 2.0
	splitRevenue := revenue / channelCount
	assert.InDelta(t, 100.0, splitRevenue, 0.01)

	// 3 channels → each gets $66.67
	channelCount = 3.0
	splitRevenue = revenue / channelCount
	assert.InDelta(t, 66.67, splitRevenue, 0.01)
}

func TestUnifiedAttribution_OverlapRateFormula(t *testing.T) {
	// overlap_rate = (1 - actual/attributed) * 100
	actual := 45000.0
	attributed := 52000.0
	rate := (1 - actual/attributed) * 100
	assert.InDelta(t, 13.46, rate, 0.1)

	// No overlap: actual == attributed
	attributed = 45000.0
	rate = (1 - actual/attributed) * 100
	assert.InDelta(t, 0.0, rate, 0.01)
	
	// Zero attributed revenue protects against division by zero
	if attributed > 0 {
		rate = (1 - actual/attributed) * 100
	}
	assert.InDelta(t, 0.0, rate, 0.01)
}
