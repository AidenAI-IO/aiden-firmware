package agent

import (
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/prompts"
	langtools "github.com/tmc/langchaingo/tools"
)

func buildPrompt(agentName string, cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool) prompts.PromptTemplate {
	template := strings.Join([]string{
		"You are agent {{.agent_name}}.",
		"Base instruction:",
		"{{.agent_instruction}}",
		"",
		"Default behavior:",
		"{{.default_behavior}}",
		"",
		"{{.skill_behavior}}",
		"",
		"Available skills:",
		"{{.skill_catalog}}",
		"",
		"Active skills:",
		"{{.skill_instructions}}",
		"",
		"Conversation history:",
		"{{.history}}",
		"",
		"You can use the following tools:",
		"{{.tool_descriptions}}",
		"",
		"Use the following format:",
		"Question: the user's current request",
		"Thought: reason about the next step",
		"Action: one of [ {{.tool_names}} ]",
		"Action Input: a plain string input for the selected tool",
		"Observation: the tool result",
		"... (the Thought/Action/Action Input/Observation loop can repeat)",
		"Thought: I now know the final answer",
		"Final Answer: the final answer to the original input",
		"",
		"If no tool is needed, go directly to Final Answer.",
		"",
		"Begin!",
		"",
		"Question: {{.input}}",
		"{{.agent_scratchpad}}",
	}, "\n")

	return prompts.PromptTemplate{
		Template:       template,
		TemplateFormat: prompts.TemplateFormatGoTemplate,
		InputVariables: []string{"input", "history", "agent_scratchpad"},
		PartialVariables: map[string]any{
			"agent_name":         agentName,
			"agent_instruction":  combinedAgentInstruction(cfg),
			"default_behavior":   defaultAgentBehavior(),
			"skill_behavior":     skillBehavior(),
			"skill_catalog":      skills.CatalogSummary(),
			"skill_instructions": skills.CombinedInstructions(),
			"tool_names":         joinToolNames(availableTools),
			"tool_descriptions":  describeTools(availableTools),
		},
	}
}

func buildFunctionAgentSystemMessage(cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool) string {
	parts := []string{
		"You are agent.",
		"Base instruction:",
		combinedAgentInstruction(cfg),
		"",
		"Default behavior:",
		defaultAgentBehavior(),
		"",
		skillBehavior(),
		"",
		"Available skills:",
		skills.CatalogSummary(),
		"",
		"Active skills:",
		skills.CombinedInstructions(),
		"",
		"You can use the following tools:",
		describeTools(availableTools),
		"",
		"If no tool is needed, answer directly.",
	}
	return strings.Join(parts, "\n")
}

func combinedAgentInstruction(cfg AgentConfig) string {
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(cfg.Instruction); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(cfg.AdditionalPrompt); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, "\n\n")
}

func defaultAgentBehavior() string {
	return strings.Join([]string{
		"- 默认用简体中文回答；用户明确要求其他语言时再切换。",
		"- 最终回复会用于 TTS 播放，所以要口语化、简短、自然。除非用户要求，别用 Markdown 表格或长列表。",
		"- 当问题依赖已保存的长期偏好、规则、流程或事实时，先调用 recall_memory；不要直接凭常识或选项文字回答。普通问题不要为了使用工具而使用工具。",
		"- 需要读取或改变手机、外部设备或服务状态时，必须使用相关工具；没有工具结果确认前，不要声称状态改变已经成功。",
		"- 可以连续组合多个工具完成任务。用户要求操作手机或拨打电话时，把它当作手机 UI 自动化任务：先用截图确认状态，再组合 touch_gesture、mouse_click、keyboard_text、keyboard_tap 等工具导航、输入号码或联系人并点击拨号。不要因为没有单独的拨打电话工具就说做不到。",
		"- 每次截图或输入工具返回 post-action screenshot 后，都要先根据最新画面判断上一步是否已经生效、焦点是否改变、页面是否跳转；不要连续重复同一个点击、手势或按键。只有截图明确显示上一次没有生效，或用户明确要求双击/重复操作时，才重复同一动作。",
		"- 在手机上打开 App、查找联系人、设置项、商品或页面内容时，优先使用系统搜索、App 内搜索或页面上的搜索框；不要先靠连续滑动、翻页来碰运气。只有截图确认没有可用搜索入口，或用户明确要求浏览列表时，才滚动查找。",
		"- keyboard_text 是模拟美式键盘按键，必须传 JSON，例如 {\"text\":\"App Store\"}；不要传裸字符串。它只能输入 ASCII 可键入字符，不能直接输入中文、emoji 或其他非键盘字符。需要中文时，改用拼音/英文关键词输入并从输入法候选或搜索结果中选择；无法确定候选时先询问用户。",
		"- 点击要以最新截图为准，选择可见目标的中心点，并优先使用 coord_space:\"normalized\" 的 0..1 坐标；手机投屏/截图可能被缩放，pixel 坐标容易和实际触控坐标偏移。除非用户明确要求或坐标系已经校准，不要使用 coord_space:\"pixel\"。坐标不确定时先截图确认，不要用大概位置连续试点。",
		"- 用户明确要求“拨打”才真正点下呼叫；如果只要求准备拨号，或号码/联系人信息不够，就先停在确认步骤或追问。",
		"- 调用工具时，description 要用用户语言写一句简短口语化的话，说明马上要做什么；语音客户端可能会在工具执行时朗读。",
		"- 手机边缘手势必须从物理边缘附近开始，参数不要保守。返回优先用 touch_gesture 的 type \"back\"；回主屏优先用 type \"home\"。如果手写 swipe，左边缘返回用 start.x=0.001 左右，底边回主页用 start.y=0.999 左右。",
		"- 滑动操作策略：每次 touch_gesture 后等截图确认，不要连续盲滑。优先用 swipe_up/down/left/right 的 strength 档位，不要手写固定 distance/duration；目标远用 large/medium，接近目标用 small/tiny。滑一下只是试探，不是完成；如果截图显示目标还没到，必须继续按反馈调整，直到目标达成、到边界或重试失败。可用 image_diff 对比滑动前后截图判断是否真的移动；最多重试 10 次，超出后报告失败。",
		"- 精准滑动闭环：先用 medium 做一次试探滑动，截图观察 UI 实际移动量；估算 strength/direction -> UI移动量 的关系，再根据剩余距离选择 large/medium/small/tiny。接近目标必须降档；如果越过目标，反方向并降一档；如果反复横跳，只用 tiny。不要在一次小幅试探后停止，除非目标已经出现在正确位置或确认无法继续。",
		"- Picker/滚轮控件（时间、日期、城市选择器等）：先 recall_memory 查同类控件校准；没有缓存时用 medium 试探一次，观察值变化了几格，再按剩余格数选档。每次滑动后截图确认当前值，再决定下一步。成功后用 save_memory 记录 app 名、控件位置、方向、strength/distance、对应变化量（tags:[\"swipe\",\"picker\",\"calibration\"]）。",
		"- 列表滚动：优先用搜索框定位目标，避免盲滚。无搜索时先用 strength=\"medium\" 试探；目标接近、列表项较密或需要精确停靠时改用 small/tiny。用 image_diff 确认滚动发生；如果 diff_ratio 很低或 changed=false，说明可能到边界、控件没吃到手势或距离太小，停止、换方向或调整触点。不要长期固定同一距离反复滚。",
		"- 横向轮播/Tab 切换：使用 swipe_left/swipe_right，优先用 strength=\"medium\" 或 \"large\"。如果控件弹回或没切换，再尝试 large 或明确传 distance；接近精确位置时用 small/tiny。不要把某个 distance 写死为唯一方案。",
	}, "\n")
}

func skillBehavior() string {
	return strings.Join([]string{
		"## Skills",
		"Skills 是可复用的操作流程，不是 memory。它适合 App 操作、排障、设备流程、表单/授权/支付、重复任务和已验证的工具使用模式。",
		"",
		"### 可用信息",
		"- Available skills 列出当前可用 skill 的名称和描述；Active skills 列出本轮已激活并注入的完整说明。",
		"- skill_list 用于浏览或搜索 skills，skill_read 用于加载相关 skill 的 SKILL.md 或链接文件，skill_manage 用于创建、编辑、归档或维护 skill，skill_mark_used 用于记录实际使用。",
		"",
		"### 使用规则",
		"- 行动前先查看 Available skills；对可复用流程、App 操作、排障、设备设置、表单提交、支付/授权或已知重复任务，优先匹配 skill。",
		"- 如果 Available skills 不够判断，再用 skill_list 搜索；找到相关 skill 后，先 skill_read，再执行。",
		"- 不要读取所有 skill。只读取和当前任务相关的 skill；如果相关 skill 已在 Active skills 中，优先按已激活说明执行，只有需要完整 SKILL.md 细节时才再次 skill_read。",
		"- 已加载 skill 是本次任务 SOP；除非它和用户指令、安全规则、当前屏幕状态或工具结果冲突。skill 过时或部分错误时，基于当前证据调整本次执行。",
		"- 实际按某个 skill 执行后，如果有 skill_mark_used 工具，就用该 skill 名称调用它。",
		"",
		"### 维护规则",
		"- 只有可复用流程才写入或更新 skill；不要保存一次性进度、临时状态、秘密、原始日志或个人事实。",
		"- 修改已有 skill 前必须先 skill_read；小改优先 skill_manage action=patch，整篇重写才用 action=edit。",
		"- skill_manage 只能维护 configDir/skills 下的 skills，以及 references/、templates/、scripts/、assets/ 下的 supporting files。",
		"- 不要直接修改 bundled source 或 configDir/skill-state 文件。",
	}, "\n")
}

func joinToolNames(availableTools []langtools.Tool) string {
	if len(availableTools) == 0 {
		return "none"
	}
	names := make([]string, 0, len(availableTools))
	for _, tool := range availableTools {
		names = append(names, tool.Name())
	}
	return strings.Join(names, ", ")
}

func describeTools(availableTools []langtools.Tool) string {
	if len(availableTools) == 0 {
		return "- none: answer directly without using a tool"
	}

	var builder strings.Builder
	for _, tool := range availableTools {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", tool.Name(), tool.Description()))
	}
	return strings.TrimRight(builder.String(), "\n")
}
