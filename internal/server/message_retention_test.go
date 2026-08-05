package server

import (
	"testing"
	"time"

	"chat/server/store"
	"chat/server/store/mock_store"
	"chat/server/store/types"
	"go.uber.org/mock/gomock"
)

func TestRetireExpiredMessagesUsesConfiguredBoundaryAndBatch(t *testing.T) {
	controller := gomock.NewController(t)
	messages := mock_store.NewMockMessagesPersistenceInterface(controller)
	previous := store.Messages
	store.Messages = messages
	t.Cleanup(func() { store.Messages = previous })

	messages.EXPECT().RetireExpired(gomock.Any(), 37).
		DoAndReturn(func(cutoff time.Time, _ int) ([]types.Uid, error) {
			age := types.TimeNow().Sub(cutoff)
			if age < 89*24*time.Hour || age > 91*24*time.Hour {
				t.Fatalf("清理边界 = %v，不是 90 天", age)
			}
			return []types.Uid{1, 2}, nil
		})

	retireExpiredMessages(90*24*time.Hour, 37)
}
