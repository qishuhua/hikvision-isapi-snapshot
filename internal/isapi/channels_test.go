package isapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
)

func TestOnlineChannelsAndResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ISAPI/ContentMgmt/InputProxy/channels":
			fmt.Fprint(w, `<InputProxyChannelList><InputProxyChannel><id>1</id><name>入口</name></InputProxyChannel><InputProxyChannel><id>2</id><name>出口</name></InputProxyChannel></InputProxyChannelList>`)
		case "/ISAPI/ContentMgmt/InputProxy/channels/status":
			fmt.Fprint(w, `<InputProxyChannelStatusList><InputProxyChannelStatus><id>1</id><online>true</online><streamingProxyChannelIdList><streamingProxyChannelId>101</streamingProxyChannelId></streamingProxyChannelIdList></InputProxyChannelStatus><InputProxyChannelStatus><id>2</id><online>false</online></InputProxyChannelStatus></InputProxyChannelStatusList>`)
		case "/ISAPI/Streaming/channels/101/picture":
			if r.URL.Query().Get("videoResolutionWidth") != "640" || r.URL.Query().Get("videoResolutionHeight") != "480" {
				t.Error("resolution missing")
			}
			w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(time.Second)
	camera := model.Camera{Name: "NVR", BaseURL: srv.URL, Username: "admin", Password: "secret", ChannelID: "101", EndpointMode: "streaming", Channels: "all", Width: 640, Height: 480}
	channels, err := c.OnlineChannels(context.Background(), camera)
	if err != nil || len(channels) != 1 || channels[0].ID != "101" || channels[0].Name != "入口" {
		t.Fatalf("channels=%+v err=%v", channels, err)
	}
	camera.Channels = "201"
	channels, err = c.OnlineChannels(context.Background(), camera)
	if err != nil || len(channels) != 0 {
		t.Fatalf("offline channel selected: %+v %v", channels, err)
	}
	camera.Channels = ""
	if _, err = c.Capture(context.Background(), camera); err != nil {
		t.Fatal(err)
	}
}
