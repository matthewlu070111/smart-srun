package wireless

import (
	"encoding/json"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// ListChange replaces a whole UCI list, retaining order and item boundaries.
// Empty lists must be expressed as Delete; UCI cannot retain an empty list.
func ListChange(key Key, items ...string) Change {
	data, _ := json.Marshal(items)
	return Change{Key: key, Text: string(data), IsList: true}
}

func listItems(text string) ([]string, error) {
	var items []string
	if err := json.Unmarshal([]byte(text), &items); err != nil || len(items) == 0 {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "UCI 列表必须有至少一项")
	}
	for _, item := range items {
		if item == "" {
			return nil, domain.Errorf(domain.CodeInvalidArgument, "UCI 列表不能包含空项")
		}
		if _, err := quoteBatch(item); err != nil {
			return nil, err
		}
	}
	return items, nil
}
