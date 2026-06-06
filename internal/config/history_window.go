package config

// MaxHistoryDepth returns the deepest prior-period window any rank needs (max
// of window.window_periods, and after Phase 2 tenure.periods); 0 when no rank
// has a time gate. The periodic driver (HEU-501) uses it to size the axis.
func MaxHistoryDepth(plan *CompensationPlan) int {
	maxDepth := 0
	for _, r := range plan.Ranks {
		if w := r.Qualification.Window; w != nil && int(w.WindowPeriods) > maxDepth {
			maxDepth = int(w.WindowPeriods)
		}
		// Phase 2 (Task 17) extends this with r.Qualification.Tenure.Periods.
	}
	return maxDepth
}
