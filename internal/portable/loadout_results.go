package portable

const (
	LoadoutListSchemaVersion            = "portable-memory-loadout-list/v1alpha1"
	LoadoutUseVerificationSchemaVersion = "portable-memory-loadout-use-verification/v1alpha1"
)

type LoadoutStatus struct {
	Loadout Loadout `json:"loadout"`
	Current bool    `json:"current"`
	Issue   string  `json:"issue,omitempty"`
}

type LoadoutListResult struct {
	SchemaVersion string             `json:"schema_version"`
	Loadouts      []LoadoutStatus    `json:"loadouts"`
	Repository    VerificationReport `json:"repository"`
	Privacy       string             `json:"privacy"`
}

type LoadoutUseVerification struct {
	SchemaVersion string             `json:"schema_version"`
	LoadoutID     string             `json:"loadout_id"`
	Current       bool               `json:"current"`
	Issue         string             `json:"issue,omitempty"`
	Repository    VerificationReport `json:"repository"`
	Privacy       string             `json:"privacy"`
}

func ListLoadoutStatus(root string) LoadoutListResult {
	result := LoadoutListResult{
		SchemaVersion: LoadoutListSchemaVersion,
		Loadouts:      []LoadoutStatus{},
		Privacy:       PortablePrivacy,
	}
	loadouts, report := ListLoadouts(root)
	result.Repository = report
	if len(report.Issues) != 0 {
		return result
	}
	for _, item := range loadouts {
		status := LoadoutStatus{Loadout: item}
		if _, _, err := LoadCurrentLoadout(root, item.LoadoutID); err != nil {
			status.Issue = err.Error()
		} else {
			status.Current = true
		}
		result.Loadouts = append(result.Loadouts, status)
	}
	return result
}

func VerifyLoadoutUse(root, loadoutID string) LoadoutUseVerification {
	result := LoadoutUseVerification{
		SchemaVersion: LoadoutUseVerificationSchemaVersion,
		LoadoutID:     loadoutID,
		Privacy:       PortablePrivacy,
	}
	_, report, err := LoadCurrentLoadout(root, loadoutID)
	result.Repository = report
	if err != nil {
		result.Issue = err.Error()
		return result
	}
	result.Current = true
	return result
}
