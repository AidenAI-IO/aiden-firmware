package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type SkillManager struct {
	index           *SkillIndex
	mu              sync.RWMutex
	activatedSkills map[string]*SkillDefinition
}

type ResolvedSkills struct {
	Names               []string
	PreferredModel      string
	Instructions        []string
	AllowedTools        map[string]struct{}
	AllowedChildren     map[string]struct{}
	HasToolRestriction  bool
	HasChildRestriction bool
	manager             *SkillManager
}

func NewSkillManager(index *SkillIndex) *SkillManager {
	return &SkillManager{
		index:           index,
		activatedSkills: make(map[string]*SkillDefinition),
	}
}

// GetIndex returns the skill index for discovery
func (m *SkillManager) GetIndex() *SkillIndex {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.index
}

func (m *SkillManager) ReplaceIndex(index *SkillIndex) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.index = index
	for name := range m.activatedSkills {
		if skill, ok := index.Get(name); ok {
			m.activatedSkills[name] = skill
		} else {
			delete(m.activatedSkills, name)
		}
	}
}

func (m *SkillManager) Snapshot() *SkillManager {
	m.mu.RLock()
	defer m.mu.RUnlock()

	activated := make(map[string]*SkillDefinition, len(m.activatedSkills))
	for name, skill := range m.activatedSkills {
		activated[name] = skill
	}
	return &SkillManager{
		index:           m.index,
		activatedSkills: activated,
	}
}

// Activate loads the full instructions for a skill by name
func (m *SkillManager) Activate(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already activated
	if _, ok := m.activatedSkills[name]; ok {
		return nil
	}

	// Get skill from index
	skill, ok := m.index.Get(name)
	if !ok {
		return fmt.Errorf("unknown skill %q", name)
	}

	// Mark as activated
	m.activatedSkills[name] = skill
	return nil
}

// Resolve merges all activated skills into a single ResolvedSkills
func (m *SkillManager) Resolve(names []string) (ResolvedSkills, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	resolved := ResolvedSkills{
		Names:           uniqueNonEmpty(names),
		AllowedTools:    map[string]struct{}{},
		AllowedChildren: map[string]struct{}{},
		manager:         m,
	}

	for _, name := range resolved.Names {
		skill, ok := m.activatedSkills[name]
		if !ok {
			return ResolvedSkills{}, fmt.Errorf("skill %q not activated", name)
		}

		if text := strings.TrimSpace(skill.Instructions); text != "" {
			resolved.Instructions = append(resolved.Instructions, fmt.Sprintf("[%s] %s", name, text))
		}

		if skill.PreferredModel != "" {
			resolved.PreferredModel = skill.PreferredModel
		}

		if len(skill.AllowedTools) > 0 {
			resolved.HasToolRestriction = true
			for _, tool := range skill.AllowedTools {
				resolved.AllowedTools[tool] = struct{}{}
			}
		}

		if len(skill.AllowedChildren) > 0 {
			resolved.HasChildRestriction = true
			for _, child := range skill.AllowedChildren {
				resolved.AllowedChildren[child] = struct{}{}
			}
		}
	}

	return resolved, nil
}

// GetActivatedSkills returns all currently activated skills
func (m *SkillManager) GetActivatedSkills() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.activatedSkills))
	for name := range m.activatedSkills {
		names = append(names, name)
	}
	return names
}

func (r ResolvedSkills) CombinedInstructions() string {
	if len(r.Instructions) == 0 {
		return "No extra skill is active."
	}
	return strings.Join(r.Instructions, "\n")
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
