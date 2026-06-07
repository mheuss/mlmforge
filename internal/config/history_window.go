package config

// MaxHistoryDepth returns the deepest prior-period window any rank needs (max
// of window.window_periods and tenure.periods across all ranks); 0 when no rank
// has a time gate. The periodic driver (HEU-501) uses it to size the axis.
func MaxHistoryDepth(plan *CompensationPlan) int {
	maxDepth := 0
	for _, r := range plan.Ranks {
		if w := r.Qualification.Window; w != nil && int(w.WindowPeriods) > maxDepth {
			maxDepth = int(w.WindowPeriods)
		}
		if t := r.Qualification.Tenure; t != nil && int(t.Periods) > maxDepth {
			maxDepth = int(t.Periods)
		}
	}
	return maxDepth
}
