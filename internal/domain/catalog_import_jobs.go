package domain

const JobCatalogImport = "catalog_import"
const CatalogImportBatchLimit = 100

type CatalogImportSelection struct {
	EntryID  string `json:"entryId"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	ParentID string `json:"parentId,omitempty"`
}

func (s CatalogImportSelection) Input() InventoryImportInput {
	return InventoryImportInput{Title: s.Title, Kind: s.Kind, ParentID: s.ParentID}
}

func ValidCatalogImportSelections(items []CatalogImportSelection) bool {
	if len(items) < 1 || len(items) > CatalogImportBatchLimit {
		return false
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !ValidID(item.EntryID) || seen[item.EntryID] || !ValidInventoryImportInput(item.Input()) {
			return false
		}
		seen[item.EntryID] = true
	}
	return true
}

type CatalogImportTask struct {
	Sequence int
	Source   InventoryImportSource
	Input    InventoryImportInput
}

type CatalogImportEntryResult struct {
	EntryID   string `json:"entryId"`
	Completed bool   `json:"completed"`
	ItemID    string `json:"itemId,omitempty"`
	SourceID  string `json:"sourceId,omitempty"`
}

type CatalogImportReport struct {
	JobID     string                     `json:"jobId"`
	Total     int                        `json:"total"`
	Completed int                        `json:"completed"`
	Entries   []CatalogImportEntryResult `json:"entries"`
}
