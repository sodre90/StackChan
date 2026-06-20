/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package web_socket

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// makeMsg builds a custom-protocol frame: 1-byte type, 4-byte big-endian
// declared length, then body. declaredLen is set independently of len(body)
// so tests can exercise malformed (mismatched) headers.
func makeMsg(msgType byte, declaredLen uint32, body []byte) []byte {
	msg := make([]byte, 5+len(body))
	msg[0] = msgType
	binary.BigEndian.PutUint32(msg[1:5], declaredLen)
	copy(msg[5:], body)
	return msg
}

func TestParseBinaryMessage(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		msg      []byte
		wantOK   bool
		wantType byte
		wantLen  int
	}{
		{"too short", []byte{0x07, 0x00, 0x00}, false, 0, 0},
		{"valid empty payload", makeMsg(0x07, 0, nil), true, 0x07, 0},
		{"valid with payload", makeMsg(0x07, 3, []byte("abc")), true, 0x07, 3},
		// Header claims more bytes than the body delivers. The old code sliced
		// before validating the length, panicking with slice-bounds-out-of-range.
		{"declared longer than body", makeMsg(0x07, 255, nil), false, 0, 0},
		{"declared shorter than body", makeMsg(0x07, 1, []byte("abc")), false, 0, 0},
		// Attacker-controlled max uint32 length must not panic.
		{"declared max uint32", makeMsg(0x07, 0xFFFFFFFF, []byte("abc")), false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.msg
			gotType, gotLen, gotPayload, ok := parseBinaryMessage(ctx, &m)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if gotType != tt.wantType {
				t.Errorf("type = %d, want %d", gotType, tt.wantType)
			}
			if gotLen != tt.wantLen {
				t.Errorf("len = %d, want %d", gotLen, tt.wantLen)
			}
			if len(gotPayload) != tt.wantLen {
				t.Errorf("payload len = %d, want %d", len(gotPayload), tt.wantLen)
			}
		})
	}
}

// TestConcurrentCameraSubscription exercises the camera-subscription state from
// both sides at once: the device streaming JPEG frames (which reads
// CameraSubscriptionList) while Apps subscribe/unsubscribe (which mutate it).
// Run with -race; before the locking fix the JPEG path read the slice without
// holding client.mu, racing the locked append/rebuild on the App path.
// Conns are nil throughout — forwardMessage treats nil as offline and no-ops.
func TestConcurrentCameraSubscription(t *testing.T) {
	ctx := context.Background()
	mac := "RACEMAC00001"
	sc := &StackChanClient{Mac: mac, mu: &sync.RWMutex{}}
	stackChanClientPool.Store(mac, sc)
	defer stackChanClientPool.Delete(mac)

	bin := websocket.BinaryMessage
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Device side: stream JPEG frames continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		jpeg := createMessage(Jpeg, []byte("frame"))
		for {
			select {
			case <-stop:
				return
			default:
				readStackChanMessage(ctx, sc, &bin, jpeg)
			}
		}
	}()

	// Reconnect churn: rewrite the device's Conn under the lock, racing the
	// forwardMessage reads of sc.Conn. nil is fine — forwardMessage skips it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				sc.mu.Lock()
				sc.Conn = nil
				sc.mu.Unlock()
			}
		}
	}()

	// App side: several apps churn their subscription.
	for i := 0; i < 4; i++ {
		ac := &AppClient{Mac: mac, mu: &sync.RWMutex{}, DeviceId: fmt.Sprintf("d%d", i)}
		wg.Add(1)
		go func(ac *AppClient) {
			defer wg.Done()
			on := createMessage(OnCamera, []byte(mac))
			off := createMessage(OffCamera, []byte(mac))
			for {
				select {
				case <-stop:
					return
				default:
					readAppClientMessage(ctx, ac, &bin, on)
					readAppClientMessage(ctx, ac, &bin, off)
				}
			}
		}(ac)
	}

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestConcurrentCallState exercises the App-side call/phone-screen paths against
// the device-side call handler. It covers CallAppClient and phoneScreen, which
// the App-side HangupCall/Jpeg cases touched without holding the lock (and where
// HangupCall additionally forwarded while holding the lock — a self-deadlock).
// Must complete and be -race clean.
func TestConcurrentCallState(t *testing.T) {
	ctx := context.Background()
	mac := "RACEMAC00002"
	sc := &StackChanClient{Mac: mac, mu: &sync.RWMutex{}}
	stackChanClientPool.Store(mac, sc)
	defer stackChanClientPool.Delete(mac)

	bin := websocket.BinaryMessage
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Device side: accept then refuse calls (reads/writes CallAppClient + list).
	wg.Add(1)
	go func() {
		defer wg.Done()
		agree := createMessage(AgreeCall, nil)
		refuse := createMessage(RefuseCall, nil)
		for {
			select {
			case <-stop:
				return
			default:
				readStackChanMessage(ctx, sc, &bin, agree)
				readStackChanMessage(ctx, sc, &bin, refuse)
			}
		}
	}()

	// App side: request/hang up calls and toggle the phone screen while pushing a
	// phone-screen JPEG (whose handler reads stackChanClient.phoneScreen).
	for i := 0; i < 4; i++ {
		ac := &AppClient{Mac: mac, mu: &sync.RWMutex{}, DeviceId: fmt.Sprintf("c%d", i)}
		wg.Add(1)
		go func(ac *AppClient) {
			defer wg.Done()
			req := createMessage(RequestCall, []byte(mac))
			hang := createMessage(HangupCall, nil)
			onPS := createMessage(OnPhoneScreen, []byte(mac))
			offPS := createMessage(OffPhoneScreen, []byte(mac))
			jpeg := createMessage(Jpeg, append([]byte(mac), 'x'))
			for {
				select {
				case <-stop:
					return
				default:
					readAppClientMessage(ctx, ac, &bin, req)
					readAppClientMessage(ctx, ac, &bin, onPS)
					readAppClientMessage(ctx, ac, &bin, jpeg)
					readAppClientMessage(ctx, ac, &bin, offPS)
					readAppClientMessage(ctx, ac, &bin, hang)
				}
			}
		}(ac)
	}

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}
