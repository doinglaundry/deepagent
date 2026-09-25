package thread

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/runtime/agentthread"
	inputpkg "eino-cli/deepagent/protocol/input"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestManagerMessageIDSurvivesInputDecoding(t *testing.T) {
	command, err := decodeUserInputCommand(&TransportMessage{
		ID:      "2000000000000000042",
		Type:    MessageTypeInput,
		Payload: []byte(`{"parts":[{"type":"text","text":"hello"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := MessageID(command.schema); got != "2000000000000000042" {
		t.Fatalf("message id = %q", got)
	}
	if command.input.Parts[0].Type != inputpkg.MessagePartTypeText {
		t.Fatalf("unexpected input: %+v", command.input)
	}
}

func TestInputDecodingPreservesMultimediaParts(t *testing.T) {
	command, err := decodeUserInputCommand(&TransportMessage{
		ID:   "43",
		Type: MessageTypeInput,
		Payload: []byte(`{"parts":[
			{"type":"text","text":"describe"},
			{"type":"image","url":"https://example.test/photo.png","mime_type":"image/png"}
		]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := command.schema.UserInputMultiContent
	if len(parts) != 2 || parts[1].Image == nil || parts[1].Image.URL == nil || *parts[1].Image.URL != "https://example.test/photo.png" {
		t.Fatalf("multimedia input was flattened: %+v", command.schema)
	}
}

func TestThread_MultimodalRoundTrip(t *testing.T) {
	command, err := decodeUserInputCommand(&TransportMessage{
		ID:   "9007199254740993",
		Type: MessageTypeInput,
		Payload: []byte(`{"parts":[
			{"type":"text","text":"describe","extra":{"language":"zh"}},
			{"type":"image","url":"https://example.test/photo.png","mime_type":"image/png","detail":"high"},
			{"type":"audio","base64_data":"YXVkaW8=","mime_type":"audio/wav"},
			{"type":"video","url":"https://example.test/clip.mp4","mime_type":"video/mp4"},
			{"type":"file","url":"https://example.test/report.pdf","name":"report.pdf","mime_type":"application/pdf"}
		]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	copy := graph.CopyMessage(command.schema)
	if got := MessageID(copy); got != "9007199254740993" {
		t.Fatalf("message ID changed: %q", got)
	}
	want := inputPartsForEvent(command.input.Parts)
	if got := schemaUserMessageToProtocolParts(copy); !reflect.DeepEqual(got, want) {
		t.Fatalf("multimodal event parts changed: got %+v, want %+v", got, want)
	}
}

func TestThread_RedeliveryPreservesMessageIdentity(t *testing.T) {
	ctx := context.Background()
	store := &redeliveryHistoryStore{}
	thread := agentthread.New("thread", &agentthread.RunConfig{Agent: graph.Config{Model: &redeliveryModel{}}}, make(chan agentthread.Event, 128), agentthread.ThreadOptions{HistoryStore: store, HistoryRecordID: func(_ context.Context, _, _ string, message *schema.Message) int64 {
		id, err := strconv.ParseInt(MessageID(message), 10, 64)
		if err != nil {
			return 0 // Assistant messages get a store-generated ID.
		}
		return id
	}})
	if err := thread.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		command, err := decodeUserInputCommand(&TransportMessage{ID: "9007199254740993", Type: MessageTypeInput, Payload: []byte(`{"parts":[{"type":"text","text":"hello"}]}`)})
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := thread.SubmitInput(ctx, graph.CopyMessage(command.schema))
		if err != nil {
			t.Fatal(err)
		}
		if err := accepted.RunHandle.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	userCount := 0
	for _, record := range store.records {
		if record.Message != nil && record.Message.Role == schema.User {
			userCount++
			if record.MessageID != 9007199254740993 || MessageID(record.Message) != "9007199254740993" {
				t.Fatalf("redelivery changed message identity: %+v", record)
			}
		}
	}
	if userCount != 1 {
		t.Fatalf("redelivery wrote %d user messages", userCount)
	}
}

type redeliveryModel struct{}

func (m *redeliveryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *redeliveryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("done", nil), nil
}
func (m *redeliveryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
}

type redeliveryHistoryStore struct{ records []*agentthread.HistoryRecord }

func (s *redeliveryHistoryStore) Append(_ context.Context, record *agentthread.HistoryRecord) error {
	record.Seq = int64(len(s.records) + 1)
	s.records = append(s.records, record)
	return nil
}
func (s *redeliveryHistoryStore) List(_ context.Context, query agentthread.ListQuery) ([]*agentthread.HistoryRecord, error) {
	var result []*agentthread.HistoryRecord
	for _, record := range s.records {
		if query.AfterID == nil || record.Seq > *query.AfterID {
			result = append(result, record)
		}
	}
	return result, nil
}
