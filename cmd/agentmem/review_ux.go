package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func runReviewList(args []string) error {
	flags := flag.NewFlagSet("review list", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	generationName := flags.String("candidates", "", "candidate generation path or directory name; defaults to current")
	var values repeatedStrings
	flags.Var(&values, "status", "candidate derivation status: review_ready, untrusted, or quarantined; repeatable")
	limit := flags.Int("limit", 50, "maximum candidates to return (1-1000)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("review list requires --root")
	}
	statuses := []candidates.ReviewStatus{}
	if len(values) == 0 {
		statuses = append(statuses, candidates.StatusReviewReady)
	} else {
		for _, value := range values {
			statuses = append(statuses, candidates.ReviewStatus(value))
		}
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	generation, err := resolveCandidateGeneration(store, *generationName)
	if err != nil {
		return err
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return err
	}
	result, err := generation.List(candidates.ListOptions{Statuses: statuses, Limit: *limit})
	if err != nil {
		return err
	}
	return encodeIndented(result)
}

func runReviewDecide(args []string) error {
	flags := flag.NewFlagSet("review decide", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	generationName := flags.String("candidates", "", "candidate generation path or directory name; defaults to current")
	candidateID := flags.String("candidate", "", "candidate id (required)")
	actionValue := flags.String("action", "", "validate, reject, quarantine, or reopen (required)")
	reviewerID := flags.String("reviewer", "", "caller attestation id (required)")
	reviewerKind := flags.String("reviewer-kind", review.ReviewerKindCallerAttestation, "caller_attestation or synthetic_test")
	scopeKind := flags.String("scope", "", "global, agent, repository, project, or task")
	scopeValue := flags.String("scope-value", "", "scope value; use * for global")
	reason := flags.String("reason", "", "review attestation reason (required)")
	var basisValues, evidenceValues repeatedStrings
	flags.Var(&basisValues, "basis", "explicit_remember, user_correction, stable_repetition, outcome_evidence, or explicit_user_confirmation; repeatable")
	flags.Var(&evidenceValues, "evidence-event", "additional evidence event id; repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *candidateID == "" || *actionValue == "" ||
		strings.TrimSpace(*reviewerID) == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("review decide requires --root, --candidate, --action, --reviewer, and --reason")
	}
	action, err := reviewAction(*actionValue)
	if err != nil {
		return err
	}
	if *reviewerKind != review.ReviewerKindCallerAttestation && *reviewerKind != review.ReviewerKindSyntheticTest {
		return errors.New("reviewer-kind must be caller_attestation or synthetic_test")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	generation, err := resolveCandidateGeneration(store, *generationName)
	if err != nil {
		return err
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return err
	}
	selection, err := generation.Select([]string{*candidateID})
	if err != nil {
		return err
	}
	candidate := selection.Candidates[*candidateID]
	if candidate.ConflictGroupID != "" {
		return errors.New("conflicted candidates require one atomic review apply request for the complete conflict group")
	}
	current, err := review.GetStatus(store, generation.Name, candidate.CandidateID)
	if err != nil {
		return err
	}
	transition := review.TransitionRequest{CandidateID: candidate.CandidateID,
		CandidateContentSHA256: candidate.ContentSHA256, ExpectedStatus: current.ReviewStatus,
		Action: action, Reason: *reason}
	for _, value := range basisValues {
		basis, err := reviewBasis(value)
		if err != nil {
			return err
		}
		transition.Basis = append(transition.Basis, basis)
	}
	sort.Slice(transition.Basis, func(left, right int) bool { return transition.Basis[left] < transition.Basis[right] })
	transition.EvidenceEventIDs = sortedUnique(evidenceValues)
	if action == review.ActionValidate {
		scope, err := reviewScope(*scopeKind, *scopeValue)
		if err != nil {
			return err
		}
		transition.Scope = &scope
	}
	result, err := review.Apply(store, generation.Name, review.Request{
		SchemaVersion: review.RequestSchemaVersion,
		Reviewer:      review.Reviewer{Kind: *reviewerKind, ID: *reviewerID},
		Transitions:   []review.TransitionRequest{transition},
	})
	if err != nil {
		return err
	}
	return encodeIndented(result)
}

func runPromoteCandidate(args []string) error {
	flags := flag.NewFlagSet("promote candidate", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	generationName := flags.String("candidates", "", "candidate generation path or directory name; defaults to current")
	candidateID := flags.String("candidate", "", "validated candidate id (required)")
	approverID := flags.String("approver", "", "caller attestation id (required)")
	approverKind := flags.String("approver-kind", promotion.ApproverKindCallerAttestation, "caller_attestation or synthetic_test")
	confirmedTextSHA := flags.String("confirm-text-sha256", "", "SHA-256 of the exact candidate text shown by review list (required)")
	reason := flags.String("reason", "", "promotion reason (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *candidateID == "" ||
		strings.TrimSpace(*approverID) == "" || *confirmedTextSHA == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("promote candidate requires --root, --candidate, --approver, --confirm-text-sha256, and --reason")
	}
	if *approverKind != promotion.ApproverKindCallerAttestation && *approverKind != promotion.ApproverKindSyntheticTest {
		return errors.New("approver-kind must be caller_attestation or synthetic_test")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	generation, err := resolveCandidateGeneration(store, *generationName)
	if err != nil {
		return err
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return err
	}
	selection, err := generation.Select([]string{*candidateID})
	if err != nil {
		return err
	}
	candidate := selection.Candidates[*candidateID]
	status, err := review.GetStatus(store, generation.Name, candidate.CandidateID)
	if err != nil {
		return err
	}
	if status.ReviewStatus != review.StatusValidated || status.LastReviewRecordSHA256 == "" {
		return errors.New("candidate is not currently validated by a caller attestation")
	}
	validated, err := review.ResolveCurrentValidation(
		store, generation.Name, candidate.CandidateID, candidate.ContentSHA256, status.LastReviewRecordSHA256,
	)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(validated.Candidate.Text))
	textSHA := hex.EncodeToString(digest[:])
	if *confirmedTextSHA != textSHA {
		return errors.New("confirmed text hash does not match the exact validated candidate text")
	}
	scan, err := promotion.ScanCandidate(store, generation.Name, candidate.CandidateID)
	if err != nil {
		return err
	}
	if len(scan.Report.Findings) != 0 {
		return fmt.Errorf("candidate contains %d sensitive finding(s); use promote apply with explicit exact redactions", len(scan.Report.Findings))
	}
	result, err := promotion.Apply(store, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: *approverKind, ID: *approverID},
		Action:        promotion.ActionPromote,
		Candidate: &promotion.CandidateReference{
			Generation: generation.Name, CandidateID: validated.Candidate.CandidateID,
			CandidateContentSHA256:  validated.Candidate.ContentSHA256,
			ExpectedReviewRecordSHA: validated.Proof.ReviewRecordSHA256,
		},
		ExpectedTextSHA256: textSHA,
		Reason:             *reason,
	})
	if err != nil {
		return err
	}
	return encodeIndented(result)
}

func reviewAction(value string) (review.Action, error) {
	action := review.Action(value)
	switch action {
	case review.ActionValidate, review.ActionReject, review.ActionQuarantine, review.ActionReopen:
		return action, nil
	default:
		return "", fmt.Errorf("unsupported review action %q", value)
	}
}

func reviewBasis(value string) (review.Basis, error) {
	basis := review.Basis(value)
	switch basis {
	case review.BasisExplicitRemember, review.BasisUserCorrection, review.BasisStableRepetition,
		review.BasisOutcomeEvidence, review.BasisExplicitUserConfirmation:
		return basis, nil
	default:
		return "", fmt.Errorf("unsupported review basis %q", value)
	}
}

func reviewScope(kindValue, value string) (review.Scope, error) {
	scope := review.Scope{Kind: review.ScopeKind(kindValue), Value: value}
	switch scope.Kind {
	case review.ScopeGlobal, review.ScopeAgent, review.ScopeRepository, review.ScopeProject, review.ScopeTask:
	default:
		return review.Scope{}, fmt.Errorf("unsupported review scope %q", kindValue)
	}
	if strings.TrimSpace(value) == "" || scope.Kind == review.ScopeGlobal && value != "*" {
		return review.Scope{}, errors.New("review scope value is invalid")
	}
	return scope, nil
}

func sortedUnique(values []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
