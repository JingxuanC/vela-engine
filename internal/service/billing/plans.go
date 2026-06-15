package billing
type PlanConfigV2 struct {
    Name string; BasePrice float64; Interval string; TrialDays int
    MonthlyQuota int; OveragePrice float64; OverageBlock int
}
var AvailableV2Plans = map[string]PlanConfigV2{
    "free":    {Name: "Free", BasePrice: 0, MonthlyQuota: 100},
    "starter": {Name: "Starter", BasePrice: 29, Interval: "monthly", TrialDays: 14, MonthlyQuota: 500},
    "growth":  {Name: "Growth", BasePrice: 79, Interval: "monthly", TrialDays: 14, MonthlyQuota: 2000, OveragePrice: 10.0, OverageBlock: 500},
    "pro":     {Name: "Pro", BasePrice: 149, Interval: "monthly", TrialDays: 14, MonthlyQuota: 10000, OveragePrice: 5.0, OverageBlock: 500},
}
