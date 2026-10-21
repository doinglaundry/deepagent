package execution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"eino-cli/deepagent/graph/middleware"
	agentmodel "eino-cli/deepagent/model"
	"github.com/cloudwego/eino/schema"
)

type inspectionRequestMiddleware struct{ middleware.BaseMiddleware }

func (inspectionRequestMiddleware) WrapModel(next agentmodel.ModelHandler) agentmodel.ModelHandler {
	return func(ctx context.Context, messages []*agentmodel.Message) (*schema.StreamReader[*agentmodel.Message], error) {
		return next(ctx, append(messages, agentmodel.NewSystemMessage("actual boundary")))
	}
}

func TestModelInspectionRecordsRequestAfterModelWrapper(t *testing.T) {
	var observed []*agentmodel.Message
	graph, err := New(context.Background(), Config{Model: &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}, Middlewares: []agentmodel.Middleware{inspectionRequestMiddleware{}}, Emit: func(_ context.Context, event agentmodel.RuntimeEvent) error {
		if event.Kind == "llm_requesting" {
			observed = event.Data.(agentmodel.LLMRequestingPayload).Messages
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	_, err = graph.Invoke(context.Background(), []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("hello")}}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) == 0 || observed[len(observed)-1].Content != "actual boundary" {
		t.Fatalf("recorded the pre-wrapper request: %+v", observed)
	}
}

type inspectionResponseMiddleware struct{ middleware.BaseMiddleware }

func (inspectionResponseMiddleware) ModifyModelResponse(_ context.Context, response *agentmodel.Message, _ *agentmodel.GraphState) (*agentmodel.Message, error) {
	response.Content = "processed"
	return response, nil
}

func TestModelInspectionRetainsRawResponseBeforeMutationAndRejection(t *testing.T) {
	for _, role := range []schema.RoleType{schema.Assistant, schema.User} {
		t.Run(string(role), func(t *testing.T) {
			var observed []byte
			graph, err := New(context.Background(), Config{Model: &sequenceModel{responses: [][]*schema.Message{{{Role: role, Content: "original"}}}}, Middlewares: []agentmodel.Middleware{inspectionResponseMiddleware{}}, Emit: func(_ context.Context, event agentmodel.RuntimeEvent) error {
				if event.Kind == "llm_response" {
					observed, _ = json.Marshal(event.Data)
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer graph.Close(context.Background())
			_, invokeErr := graph.Invoke(context.Background(), []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("hello")}}, RunOptions{})
			if (invokeErr != nil) != (role == schema.User) {
				t.Fatalf("unexpected execution result: %v", invokeErr)
			}
			if !strings.Contains(string(observed), `"content":"original"`) || strings.Contains(string(observed), "processed") {
				t.Fatalf("raw response missing or mutated: %s", observed)
			}
		})
	}
}

type inspectionRetryMiddleware struct{ middleware.BaseMiddleware }

func (inspectionRetryMiddleware) WrapModel(next agentmodel.ModelHandler) agentmodel.ModelHandler {
	return func(ctx context.Context, messages []*agentmodel.Message) (*schema.StreamReader[*agentmodel.Message], error) {
		first, err := next(ctx, messages)
		if err != nil {
			return nil, err
		}
		_, err = first.Recv()
		first.Close()
		if err != nil {
			return nil, err
		}
		return next(ctx, messages)
	}
}

func TestModelInspectionSeparatesResponsesWhenWrapperRetriesAfterReading(t *testing.T) {
	var requests []string
	var responses []agentmodel.LLMResponsePayload
	graph, err := New(context.Background(), Config{Model: &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("first", nil)}, {schema.AssistantMessage("second", nil)}}}, Middlewares: []agentmodel.Middleware{inspectionRetryMiddleware{}}, Emit: func(_ context.Context, event agentmodel.RuntimeEvent) error {
		if event.Kind == "llm_requesting" {
			requests = append(requests, event.Data.(agentmodel.LLMRequestingPayload).LLMResponseID)
		}
		if event.Kind == "llm_response" {
			responses = append(responses, event.Data.(agentmodel.LLMResponsePayload))
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	_, err = graph.Invoke(context.Background(), []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("hello")}}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0] == "" || requests[0] == requests[1] || len(responses) != 2 {
		t.Fatalf("requests=%v responses=%+v", requests, responses)
	}
	for index, response := range responses {
		if response.LLMResponseID != requests[index] || len(response.Chunks) != 1 {
			t.Fatalf("response linked incorrectly: %+v", response)
		}
	}
}

type inspectionCancelMiddleware struct {
	middleware.BaseMiddleware
	cancel context.CancelFunc
}

func (current inspectionCancelMiddleware) ModifyModelResponse(_ context.Context, response *agentmodel.Message, _ *agentmodel.GraphState) (*agentmodel.Message, error) {
	current.cancel()
	return nil, context.Canceled
}

func TestModelInspectionSavesReceivedResponseAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	saved := false
	graph, err := New(ctx, Config{Model: &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("received", nil)}}}, Middlewares: []agentmodel.Middleware{inspectionCancelMiddleware{cancel: cancel}}, Emit: func(ctx context.Context, event agentmodel.RuntimeEvent) error {
		if event.Kind == "llm_response" {
			saved = ctx.Err() == nil
		}
		return ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	_, err = graph.Invoke(ctx, []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("hello")}}, RunOptions{})
	if err == nil || !saved {
		t.Fatalf("err=%v saved=%v", err, saved)
	}
}
