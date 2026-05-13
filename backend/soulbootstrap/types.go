package soulbootstrap

type State struct {
	Phase        Phase
	Round        int
	Conversation []Turn
	Fields       Fields
	Draft        string
	ExistingSoul string
}

type Phase string

const (
	PhaseHello       Phase = "hello"
	PhaseYou         Phase = "you"
	PhasePersonality Phase = "personality"
	PhaseDepth       Phase = "depth"
	PhaseDraft       Phase = "draft"
)

type Turn struct {
	Role    string
	Content string
}

type Fields struct {
	PreferredLanguage  string `json:"preferred_language"`
	UserName           string `json:"user_name"`
	UserRole           string `json:"user_role"`
	PainPoints         string `json:"pain_points"`
	AgentName          string `json:"agent_name"`
	Relationship       string `json:"relationship"`
	CoreTraits         string `json:"core_traits"`
	CommunicationStyle string `json:"communication_style"`
	PushbackPreference string `json:"pushback_preference"`
	AutonomyLevel      string `json:"autonomy_level"`
	FailurePhilosophy  string `json:"failure_philosophy"`
	LongTermVision     string `json:"long_term_vision"`
	BlindSpots         string `json:"blind_spots"`
	Boundaries         string `json:"boundaries"`
	CodeStyle          string `json:"code_style"`
	NeverDo            string `json:"never_do"`
}

type Reply struct {
	Message   string `json:"message"`
	NextPhase Phase  `json:"next_phase"`
	Fields    Fields `json:"fields"`
	Draft     string `json:"draft"`
	Ready     bool   `json:"ready"`
}
