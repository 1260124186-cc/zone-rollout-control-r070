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

	plan, _, err := createActivePlan(
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
	if plan.State != domain.PlanActive {
		return ensure(false, "plan was not activated")
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

	var completed domain.PlanDetail
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": plan.Version,
		"category":         "successful",
		"notes":            "all edges served the new serial",
	}, &completed); err != nil {
		return err
	}
	if completed.State != domain.PlanCompleted {
		return ensure(false, "plan did not complete")
	}
	if completed.Completion == nil {
		return ensure(false, "completion did not return a completion record")
	}
	if completed.Completion.Category != domain.EndSuccessful ||
		completed.Completion.Notes != "all edges served the new serial" ||
		completed.Completion.PlanVersion != completed.Version ||
		completed.Completion.ID == "" ||
		completed.Completion.CompletedAt.IsZero() {
		return ensure(false, "completion record did not capture the completion details")
	}

	// Repeating completion returns the original immutable record, even when
	// the request omits the body or sends a stale version.
	var replay domain.PlanDetail
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": completed.Version,
		"category":         "aborted",
		"notes":            "this must be ignored",
	}, &replay); err != nil {
		return err
	}
	if replay.Completion == nil || replay.Completion.ID != completed.Completion.ID ||
		replay.Completion.Category != domain.EndSuccessful ||
		replay.Completion.Notes != "all edges served the new serial" {
		return ensure(false, "completion replay overwrote the original record")
	}
	var replayNoBody domain.PlanDetail
	request, err := http.NewRequest(http.MethodPatch, harness.baseURL+"/v1/plans/"+plan.ID+"/complete", nil)
	if err != nil {
		return err
	}
	if err := harness.do(request, &replayNoBody); err != nil {
		return err
	}
	if replayNoBody.Completion == nil || replayNoBody.Completion.ID != completed.Completion.ID {
		return ensure(false, "bodyless completion replay did not return the original record")
	}

	// Plan detail reads the stored completion record.
	var detail domain.PlanDetail
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &detail); err != nil {
		return err
	}
	if detail.Completion == nil || detail.Completion.ID != completed.Completion.ID {
		return ensure(false, "plan detail did not expose the completion record")
	}

	// The list response keeps the legacy plan shape without completion records.
	var page domain.PlanPage
	if err := harness.call(http.MethodGet, "/v1/plans?state=completed", nil, &page); err != nil {
		return err
	}
	if len(page.Items) != 1 || page.Items[0].ID != plan.ID {
		return ensure(false, "completed plan was not listed")
	}

	// A second plan keeps the legacy completion shape working but records an
	// unclassified category when no category is supplied.
	legacyPlan, _, err := createActivePlan(harness, "legacy.example", 2026092503, 1, 0, 60)
	if err != nil {
		return err
	}
	var legacyCompleted domain.PlanDetail
	if err := harness.call(http.MethodPatch, "/v1/plans/"+legacyPlan.ID+"/complete", map[string]any{
		"expected_version": legacyPlan.Version,
	}, &legacyCompleted); err != nil {
		return err
	}
	if legacyCompleted.Completion == nil ||
		legacyCompleted.Completion.Category != domain.EndUnclassified {
		return ensure(false, "legacy completion request was not classified as unclassified")
	}

	// Version conflicts fail before the plan changes state.
	conflictPlan, _, err := createActivePlan(harness, "conflict.example", 2026092504, 1, 0, 60)
	if err != nil {
		return err
	}
	err = harness.call(http.MethodPatch, "/v1/plans/"+conflictPlan.ID+"/complete", map[string]any{
		"expected_version": conflictPlan.Version + 1,
		"category":         "aborted",
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeVersionConflict)); err != nil {
		return err
	}
	var stillActive domain.PlanDetail
	if err := harness.call(http.MethodGet, "/v1/plans/"+conflictPlan.ID, nil, &stillActive); err != nil {
		return err
	}
	if stillActive.State != domain.PlanActive || stillActive.Completion != nil {
		return ensure(false, "version conflict modified the plan")
	}

	// Missing assessment references fail before the plan changes state.
	err = harness.call(http.MethodPatch, "/v1/plans/"+conflictPlan.ID+"/complete", map[string]any{
		"expected_version": conflictPlan.Version,
		"category":         "successful",
		"assessment_id":    "asm_missing",
	}, nil)
	if err := expectFailure(err, http.StatusBadRequest, string(domain.CodeValidationFailed)); err != nil {
		return err
	}
	if err := harness.call(http.MethodGet, "/v1/plans/"+conflictPlan.ID, nil, &stillActive); err != nil {
		return err
	}
	if stillActive.State != domain.PlanActive || stillActive.Completion != nil {
		return ensure(false, "missing assessment reference modified the plan")
	}

	// Completing from a non-active plan remains an invalid state transition.
	draftPlan, err := harness.createDraftPlan("draft.example")
	if err != nil {
		return err
	}
	err = harness.call(http.MethodPatch, "/v1/plans/"+draftPlan.ID+"/complete", map[string]any{
		"expected_version": draftPlan.Version,
	}, nil)
	return expectFailure(err, http.StatusConflict, string(domain.CodeInvalidState))
}
