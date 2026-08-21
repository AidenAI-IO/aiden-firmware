package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

var errMemoryRevisionChanged = errors.New("memory revision changed")

// MemoryOperation describes the result of applying a candidate to a Memory
// store. It is an operation, not the persisted lifecycle status of a Memory.
type MemoryOperation string

const (
	MemoryOperationAdd       MemoryOperation = "add"
	MemoryOperationUpdate    MemoryOperation = "update"
	MemoryOperationReinforce MemoryOperation = "reinforce"
	MemoryOperationSupersede MemoryOperation = "supersede"
	MemoryOperationRemove    MemoryOperation = "remove"
	MemoryOperationIgnore    MemoryOperation = "ignore"
	MemoryOperationHold      MemoryOperation = "hold"
)

// MemoryIntent is the small seam between a scenario Processor and the
// persistence/merge implementation. Processors decide what is worth
// remembering; the store decides whether it is an add, update, replacement,
// or removal.
type MemoryIntent struct {
	Item             MemoryItem
	DeviceItem       *DeviceMemoryItem
	ExpectedRevision int
	Remove           bool
}

type MemoryApplyResult struct {
	Operation MemoryOperation
	ID        string
}

// ApplyMemoryIntent is the common write path for temporary and long-term
// filesystem memories. An explicit ID is treated as the identity of the
// record, so repeated observations update that record instead of creating a
// second file.
func (s *LongTermMemoryStore) ApplyMemoryIntent(ctx context.Context, intent MemoryIntent) (MemoryApplyResult, error) {
	if s == nil {
		return MemoryApplyResult{}, errors.New("memory store is not configured")
	}
	if intent.Remove {
		id := strings.TrimSpace(intent.Item.ID)
		if id == "" {
			return MemoryApplyResult{Operation: MemoryOperationIgnore}, nil
		}
		path := s.memoryPath(id)
		parsed, err := readMemoryMarkdown(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return MemoryApplyResult{Operation: MemoryOperationIgnore, ID: id}, nil
			}
			return MemoryApplyResult{}, err
		}
		if intent.ExpectedRevision > 0 && effectiveMemoryRevision(parsed.Item) != intent.ExpectedRevision {
			return MemoryApplyResult{}, errMemoryRevisionChanged
		}
		if parsed.Item.Status == "deleted" {
			return MemoryApplyResult{Operation: MemoryOperationIgnore, ID: id}, nil
		}
		if err := s.Forget(ctx, id, "memory intent removed"); err != nil {
			return MemoryApplyResult{}, err
		}
		return MemoryApplyResult{Operation: MemoryOperationRemove, ID: id}, nil
	}

	item := intent.Item
	if strings.TrimSpace(item.ID) != "" {
		path := s.memoryPath(item.ID)
		parsed, err := readMemoryMarkdown(path)
		if err == nil {
			if intent.ExpectedRevision > 0 && effectiveMemoryRevision(parsed.Item) != intent.ExpectedRevision {
				return MemoryApplyResult{}, errMemoryRevisionChanged
			}
			if parsed.Item.Status == "deleted" {
				// A new observation can resurrect the same identity, but it must
				// become a fresh active revision with the original ID.
				parsed.Item.Status = "active"
			}
			same := sameMemoryConclusion(parsed.Item, item)
			mergeMemoryItem(&parsed.Item, item, time.Now().UTC())
			if err := writeFileAtomic(path, []byte(formatMemoryMarkdown(parsed.Item)), 0o644); err != nil {
				return MemoryApplyResult{}, err
			}
			s.invalidateParsedMemoryCache(path)
			if err := s.RebuildIndex(ctx); err != nil {
				return MemoryApplyResult{}, err
			}
			operation := MemoryOperationUpdate
			if same {
				operation = MemoryOperationReinforce
			}
			return MemoryApplyResult{Operation: operation, ID: item.ID}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return MemoryApplyResult{}, err
		}
	}

	action, existingID, err := s.DecideAction(ctx, item)
	if err != nil {
		return MemoryApplyResult{}, err
	}
	switch action {
	case "ignore":
		return MemoryApplyResult{Operation: MemoryOperationIgnore, ID: existingID}, nil
	case "supersede":
		id, err := s.SupersedeMemory(ctx, existingID, item)
		return MemoryApplyResult{Operation: MemoryOperationSupersede, ID: id}, err
	default:
		id, err := s.AddMemory(ctx, item)
		return MemoryApplyResult{Operation: MemoryOperationAdd, ID: id}, err
	}
}

func effectiveMemoryRevision(item MemoryItem) int {
	if item.Revision > 0 {
		return item.Revision
	}
	return 1
}

func mergeMemoryItem(existing *MemoryItem, candidate MemoryItem, now time.Time) {
	if candidate.Title != "" {
		existing.Title = candidate.Title
	}
	if candidate.Content != "" {
		existing.Content = candidate.Content
	}
	if candidate.Type != "" {
		existing.Type = candidate.Type
	}
	if candidate.TimeScope != "" {
		existing.TimeScope = candidate.TimeScope
	}
	if candidate.ExpiresAt != "" {
		existing.ExpiresAt = candidate.ExpiresAt
	}
	if candidate.TTL != "" {
		existing.TTL = candidate.TTL
	}
	if candidate.Priority > existing.Priority {
		existing.Priority = candidate.Priority
	}
	if candidate.Confidence > existing.Confidence {
		existing.Confidence = candidate.Confidence
	}
	existing.Tags = mergeUniqueStrings(existing.Tags, candidate.Tags)
	existing.Entities = mergeUniqueStrings(existing.Entities, candidate.Entities)
	existing.SourceRefs = mergeMemorySourceRefs(existing.SourceRefs, candidate.SourceRefs)
	existing.EvidenceRefs = mergeMemorySourceRefs(existing.EvidenceRefs, candidate.EvidenceRefs)
	if len(candidate.EvidenceExcerpts) > 0 {
		existing.EvidenceExcerpts = mergeUniqueStrings(existing.EvidenceExcerpts, candidate.EvidenceExcerpts)
	}
	if existing.Status == "" {
		existing.Status = "active"
	}
	if existing.CreatedAt == "" {
		existing.CreatedAt = candidate.CreatedAt
	}
	existing.UpdatedAt = now.Format(time.RFC3339Nano)
	if existing.Revision <= 0 {
		existing.Revision = 1
	} else {
		existing.Revision++
	}
}

func sameMemoryConclusion(existing, candidate MemoryItem) bool {
	return strings.TrimSpace(existing.Content) == strings.TrimSpace(candidate.Content) &&
		strings.TrimSpace(existing.Title) == strings.TrimSpace(candidate.Title)
}

func mergeMemorySourceRefs(existing, candidate []MemorySourceRef) []MemorySourceRef {
	result := append([]MemorySourceRef(nil), existing...)
	for _, ref := range candidate {
		found := false
		for i := range result {
			if result[i].Type != ref.Type || result[i].ID != ref.ID {
				continue
			}
			result[i].EventIDs = mergeUniqueStrings(result[i].EventIDs, ref.EventIDs)
			found = true
			break
		}
		if !found {
			result = append(result, ref)
		}
	}
	return result
}
