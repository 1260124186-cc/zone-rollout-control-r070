package workflowprobe

import (
	"net/http"

	"solo-0014-zone-rollout-control/internal/domain"
)

func runPlanRevisionLifecycle() error {
	harness, err := newHarness()
	if err != nil {
		return err
	}
	defer harness.close()

	plan, revision, err := createActivePlan(
		harness,
		"edge.example",
		2026092501,
		2,
		1,
		80,
	)
	if err != nil {
		return err
	}
	if plan.State != domain.PlanActive || revision.Serial != 2026092501 {
		return ensure(false, "plan and revision were not activated")
	}

	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    2026092502,
		Authority: "ns1.edge.example",
		Entries: []domain.ZoneEntry{
			{Owner: "edge.example", Type: domain.EntryA, Value: "192.0.2.11"},
		},
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeInvalidState)); err != nil {
		return err
	}

	// A second active plan is used to prove rejected completions leave the
	// aggregate untouched.
	guarded, _, err := createActivePlan(harness, "guard.example", 2026092510, 1, 1, 80)
	if err != nil {
		return err
	}

	err = harness.call(http.MethodPatch, "/v1/plans/"+guarded.ID+"/complete", map[string]any{
		"expected_version": guarded.Version + 1,
		"category":         "succeeded",
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeVersionConflict)); err != nil {
		return err
	}
	err = harness.call(http.MethodPatch, "/v1/plans/"+guarded.ID+"/complete", map[string]any{
		"expected_version": guarded.Version,
		"category":         "succeeded",
		"assessment_id":    "asm-missing",
	}, nil)
	if err := expectFailure(err, http.StatusNotFound, string(domain.CodeNotFound)); err != nil {
		return err
	}
	var stillActive domain.PlanDetail
	if err := harness.call(http.MethodGet, "/v1/plans/"+guarded.ID, nil, &stillActive); err != nil {
		return err
	}
	if stillActive.State != domain.PlanActive || stillActive.Completion != nil || stillActive.Version != guarded.Version {
		return ensure(false, "rejected completion modified the plan")
	}

	// First completion with a full body writes the immutable record.
	var completed domain.PlanDetail
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", domain.PlanCompleteInput{
		ExpectedVersion: plan.Version,
		Category:        string(domain.CompletionSucceeded),
		Note:            "rollout verified across all resolvers",
	}, &completed); err != nil {
		return err
	}
	if completed.State != domain.PlanCompleted {
		return ensure(false, "plan did not complete")
	}
	if completed.Completion == nil {
		return ensure(false, "completion record was not returned")
	}
	record := *completed.Completion
	if record.PlanID != plan.ID ||
		record.Category != domain.CompletionSucceeded ||
		record.Note != "rollout verified across all resolvers" ||
		record.CompletedAt.IsZero() ||
		record.PlanVersion != completed.Version ||
		record.AssessmentID != "" {
		return ensure(false, "completion record did not preserve its fields")
	}

	// Plan detail reads back the same record.
	var fetched domain.PlanDetail
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &fetched); err != nil {
		return err
	}
	if fetched.Completion == nil || *fetched.Completion != record {
		return ensure(false, "plan detail does not expose the stored completion record")
	}

	// Repeated completion with a stale version and a different body replays
	// the original record instead of overwriting it.
	var replay domain.PlanDetail
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": plan.Version,
		"category":         "rolled_back",
		"note":             "attempted overwrite",
	}, &replay); err != nil {
		return err
	}
	if replay.Version != completed.Version || replay.Completion == nil || *replay.Completion != record {
		return ensure(false, "completion replay overwrote the original record")
	}

	// Legacy completion without a body beyond expected_version still works and
	// leaves the optional closeout fields empty.
	var legacy domain.PlanDetail
	if err := harness.call(http.MethodPatch, "/v1/plans/"+guarded.ID+"/complete", map[string]any{
		"expected_version": guarded.Version,
	}, &legacy); err != nil {
		return err
	}
	if legacy.State != domain.PlanCompleted || legacy.Completion == nil {
		return ensure(false, "legacy completion did not produce a record")
	}
	if legacy.Completion.Category != "" ||
		legacy.Completion.Note != "" ||
		legacy.Completion.AssessmentID != "" ||
		legacy.Completion.PlanVersion != legacy.Version ||
		legacy.Completion.CompletedAt.IsZero() {
		return ensure(false, "legacy completion record contains unexpected closeout fields")
	}
	return nil
}
