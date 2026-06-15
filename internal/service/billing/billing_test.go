package billing
import ("testing"; "github.com/stretchr/testify/assert"; "github.com/JingxuanC/vela-engine/internal/config")
func TestPlans_Pricing(t *testing.T) {
    assert.Equal(t, 29.0, AvailableV2Plans["starter"].BasePrice)
    assert.Equal(t, 79.0, AvailableV2Plans["growth"].BasePrice)
    assert.Equal(t, 149.0, AvailableV2Plans["pro"].BasePrice)
}
func TestPlans_Quota(t *testing.T) {
    assert.Equal(t, 100, AvailableV2Plans["free"].MonthlyQuota)
    assert.Equal(t, 500, AvailableV2Plans["starter"].MonthlyQuota)
    assert.Equal(t, 2000, AvailableV2Plans["growth"].MonthlyQuota)
    assert.Equal(t, 10000, AvailableV2Plans["pro"].MonthlyQuota)
}
func TestPlans_TrialDays(t *testing.T) {
    assert.Equal(t, 14, AvailableV2Plans["starter"].TrialDays)
    assert.Equal(t, 14, AvailableV2Plans["growth"].TrialDays)
    assert.Equal(t, 14, AvailableV2Plans["pro"].TrialDays)
}
func TestDevMock(t *testing.T) {
    c := NewShopifyClient(&config.Config{GoEnv: "development", BillingReturnURL: "http://localhost:8000/confirm"})
    r, err := c.CreateSubscription("test.myshopify.com", "tok", "Growth", "79", "sid", 14)
    assert.NoError(t, err)
    assert.NotEmpty(t, r.ConfirmationURL)
    assert.Contains(t, r.ConfirmationURL, "subscription_id=")
}
func TestCancelDevMode(t *testing.T) {
    c := NewShopifyClient(&config.Config{GoEnv: "development"})
    assert.NoError(t, c.CancelSubscription("test", "tok", "sub"))
}
