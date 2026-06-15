package externaldata

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGoogleTrendsClient(t *testing.T) {
	c := NewGoogleTrendsClient()
	assert.NotNil(t, c)
	assert.NotNil(t, c.httpClient)
}

func TestGoogleTrendsClient_GetInterestOverTime(t *testing.T) {
	c := NewGoogleTrendsClient()
	ctx := context.Background()

	t.Run("returns mock data for single keyword (no internet)", func(t *testing.T) {
		data, err := c.GetInterestOverTime(ctx, []string{"wireless earbuds"}, "US")
		require.NoError(t, err)
		require.NotNil(t, data)

		assert.Equal(t, "wireless earbuds", data.Keyword)
		assert.Equal(t, "US", data.Region)
		assert.Len(t, data.Timeline, 30)

		// Verify timeline is sorted ascending
		for i := 1; i < len(data.Timeline); i++ {
			assert.True(t, data.Timeline[i].Date.After(data.Timeline[i-1].Date) ||
				data.Timeline[i].Date.Equal(data.Timeline[i-1].Date))
		}

		// Verify values are within expected range
		for _, dp := range data.Timeline {
			assert.True(t, dp.Value >= 0 && dp.Value <= 100, "value %d out of range [0,100]", dp.Value)
		}
	})

	t.Run("uses first keyword when multiple provided", func(t *testing.T) {
		data, err := c.GetInterestOverTime(ctx, []string{"shoes", "sneakers", "boots"}, "US")
		require.NoError(t, err)
		assert.Equal(t, "shoes", data.Keyword)
	})

	t.Run("falls back to default keyword when empty list", func(t *testing.T) {
		data, err := c.GetInterestOverTime(ctx, []string{}, "GB")
		require.NoError(t, err)
		assert.Equal(t, "product", data.Keyword)
		assert.Equal(t, "GB", data.Region)
	})

	t.Run("timeline dates are in the past ~30 days", func(t *testing.T) {
		data, err := c.GetInterestOverTime(ctx, []string{"test"}, "US")
		require.NoError(t, err)
		now := time.Now().UTC()
		for _, dp := range data.Timeline {
			assert.True(t, dp.Date.Before(now) || dp.Date.Equal(now))
		}
	})
}

func TestGoogleTrendsClient_GetRelatedQueries(t *testing.T) {
	c := NewGoogleTrendsClient()
	ctx := context.Background()

	t.Run("returns related queries for a keyword (mock fallback)", func(t *testing.T) {
		queries, err := c.GetRelatedQueries(ctx, "wireless earbuds")
		require.NoError(t, err)
		require.Len(t, queries, 5)

		for _, q := range queries {
			assert.NotEmpty(t, q.Query)
			assert.True(t, q.Score >= 0 && q.Score <= 100, "score %d out of range", q.Score)
			assert.Contains(t, []string{"rising", "top", "stable"}, q.Status)
		}
	})

	t.Run("scores are descending", func(t *testing.T) {
		queries, err := c.GetRelatedQueries(ctx, "test")
		require.NoError(t, err)
		for i := 1; i < len(queries); i++ {
			assert.True(t, queries[i].Score <= queries[i-1].Score,
				"queries should be sorted by score descending")
		}
	})
}

func TestSetCache(t *testing.T) {
	c := NewGoogleTrendsClient()
	assert.Nil(t, c.cache)

	// Setting nil cache is fine
	c.SetCache(nil)
	assert.Nil(t, c.cache)
}

func TestTrendsCacheKey(t *testing.T) {
	key := trendsCacheKey("timeseries", "shoes", "US", "today12m")
	assert.Equal(t, "trends:timeseries:shoes:US:today12m", key)

	key = trendsCacheKey("related", "linen dress", "GB", "today 3-m")
	assert.Equal(t, "trends:related:linen dress:GB:today 3-m", key)
}

func TestStripTrendsPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard prefix",
			input: ")]}'\n{\"widgets\":[]}",
			want:  "{\"widgets\":[]}",
		},
		{
			name:  "no prefix",
			input: "{\"widgets\":[]}",
			want:  "{\"widgets\":[]}",
		},
		{
			name:  "empty",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripTrendsPrefix([]byte(tt.input))
			assert.Equal(t, tt.want, string(result))
		})
	}
}

func TestAvgValue(t *testing.T) {
	assert.Equal(t, 0.0, avgValue(nil))
	assert.Equal(t, 0.0, avgValue([]DataPoint{}))
	assert.Equal(t, 5.0, avgValue([]DataPoint{
		{Value: 5},
	}))
	assert.Equal(t, 10.0, avgValue([]DataPoint{
		{Value: 5},
		{Value: 15},
	}))
}

func TestParseTimelineResponse(t *testing.T) {
	c := NewGoogleTrendsClient()

	raw := []byte(`{"default":{"timelineData":[
		{"time":"1700000000","value":[50],"hasData":[true],"formattedValue":["50"],"formattedTime":"Nov 14","formattedAxisTime":"Nov 14"},
		{"time":"1700086400","value":[60],"hasData":[true],"formattedValue":["60"],"formattedTime":"Nov 15","formattedAxisTime":"Nov 15"},
		{"time":"1700172800","value":[40],"hasData":[true],"formattedValue":["40"],"formattedTime":"Nov 16","formattedAxisTime":"Nov 16"}
	]}}`)

	data, err := c.parseTimelineResponse("test", "US", raw)
	require.NoError(t, err)
	assert.Equal(t, "test", data.Keyword)
	assert.Equal(t, "US", data.Region)
	assert.Len(t, data.Timeline, 3)
	assert.Equal(t, 50, data.Timeline[0].Value)
	assert.Equal(t, 60, data.Timeline[1].Value)
	assert.Equal(t, 40, data.Timeline[2].Value)
	assert.Equal(t, 60, data.PeakScore)
}

func TestParseRelatedQueriesResponse(t *testing.T) {
	c := NewGoogleTrendsClient()

	raw := []byte(`{"default":{"rankedList":[
		{"rankedKeyword":[{"query":"shoes sale","value":100,"hasData":true,"link":"/trends/explore?q=shoes+sale","formattedValue":"100"}]},
		{"rankedKeyword":[{"query":"running shoes","value":80,"hasData":true,"link":"/trends/explore?q=running+shoes","formattedValue":"80"}]}
	]}}`)

	queries, err := c.parseRelatedQueriesResponse(raw)
	require.NoError(t, err)
	assert.Len(t, queries, 2)
	assert.Equal(t, "shoes sale", queries[0].Query)
	assert.Equal(t, 100, queries[0].Score)
	assert.Equal(t, "running shoes", queries[1].Query)
	assert.Equal(t, 80, queries[1].Score)
}

func TestRateLimit(t *testing.T) {
	c := NewGoogleTrendsClient()
	start := time.Now()

	c.rateLimit()
	c.rateLimit()

	elapsed := time.Since(start)
	assert.True(t, elapsed >= trendsRequestPause,
		"expected at least %v pause, got %v", trendsRequestPause, elapsed)
}
