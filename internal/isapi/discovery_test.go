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

func TestDiscoveryIncludesOfflineChannels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ISAPI/System/deviceInfo":
			fmt.Fprint(w, `<DeviceInfo><deviceName>仓库NVR</deviceName><model>test</model></DeviceInfo>`)
		case "/ISAPI/ContentMgmt/InputProxy/channels":
			fmt.Fprint(w, `<InputProxyChannelList><InputProxyChannel><id>1</id><name>入口</name></InputProxyChannel><InputProxyChannel><id>2</id><name>出口</name></InputProxyChannel></InputProxyChannelList>`)
		case "/ISAPI/ContentMgmt/InputProxy/channels/status":
			fmt.Fprint(w, `<InputProxyChannelStatusList><InputProxyChannelStatus><id>1</id><online>true</online><streamingProxyChannelIdList><streamingProxyChannelId>901</streamingProxyChannelId></streamingProxyChannelIdList></InputProxyChannelStatus><InputProxyChannelStatus><id>2</id><online>false</online></InputProxyChannelStatus></InputProxyChannelStatusList>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := New(time.Second)
	camera := model.Camera{BaseURL: server.URL}
	result, err := c.Discover(context.Background(), camera, "nvr")
	if err != nil {
		t.Fatal(err)
	}
	if result.DeviceName != "仓库NVR" || len(result.Channels) != 2 || result.Channels[0].StreamID != "901" || result.Channels[0].Name != "入口" || result.Channels[0].Online == nil || !*result.Channels[0].Online || result.Channels[1].Online == nil || *result.Channels[1].Online {
		t.Fatalf("unexpected discovery: %+v", result)
	}
	camera.Channels = "901"
	channels, err := c.OnlineChannels(context.Background(), camera)
	if err != nil || len(channels) != 1 || channels[0].ID != "901" {
		t.Fatalf("selected channels: %+v %v", channels, err)
	}
	result, err = c.Discover(context.Background(), camera, "camera")
	if err != nil || result.DeviceName != "仓库NVR" || len(result.Channels) != 0 {
		t.Fatalf("direct camera: %+v %v", result, err)
	}
}
