package isapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
)

type DiscoveredChannel struct {
	LogicalID int    `json:"logical_id"`
	StreamID  string `json:"stream_id"`
	Name      string `json:"name"`
	Online    *bool  `json:"online"`
}
type Discovery struct {
	DeviceName string              `json:"device_name"`
	DeviceType string              `json:"device_type"`
	Model      string              `json:"model"`
	Channels   []DiscoveredChannel `json:"channels"`
}

func (c *Client) readXML(ctx context.Context, camera model.Camera, path string, dst any) error {
	resp, err := c.get(ctx, camera, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("%s: response too large", path)
	}
	return xml.Unmarshal(data, dst)
}

func (c *Client) Discover(ctx context.Context, camera model.Camera, kind string) (Discovery, error) {
	var info struct {
		XMLName xml.Name
		Name    string `xml:"deviceName"`
		Model   string `xml:"model"`
		Type    string `xml:"deviceType"`
	}
	if err := c.readXML(ctx, camera, "/ISAPI/System/deviceInfo", &info); err != nil {
		return Discovery{}, err
	}
	if info.XMLName.Local != "DeviceInfo" {
		return Discovery{}, fmt.Errorf("unexpected device info response")
	}
	result := Discovery{DeviceName: strings.TrimSpace(info.Name), DeviceType: strings.TrimSpace(info.Type), Model: strings.TrimSpace(info.Model), Channels: []DiscoveredChannel{}}
	if kind == "camera" {
		return result, nil
	}
	if kind != "nvr" {
		return Discovery{}, fmt.Errorf("device_type must be nvr or camera")
	}
	var names struct {
		XMLName xml.Name
		Items   []struct {
			ID   int    `xml:"id"`
			Name string `xml:"name"`
		} `xml:"InputProxyChannel"`
	}
	if err := c.readXML(ctx, camera, "/ISAPI/ContentMgmt/InputProxy/channels", &names); err != nil {
		return Discovery{}, err
	}
	if names.XMLName.Local != "InputProxyChannelList" {
		return Discovery{}, fmt.Errorf("unexpected channel list")
	}
	var statuses struct {
		XMLName xml.Name
		Items   []struct {
			ID      int    `xml:"id"`
			Online  string `xml:"online"`
			Streams []int  `xml:"streamingProxyChannelIdList>streamingProxyChannelId"`
		} `xml:"InputProxyChannelStatus"`
	}
	if err := c.readXML(ctx, camera, "/ISAPI/ContentMgmt/InputProxy/channels/status", &statuses); err != nil {
		return Discovery{}, err
	}
	if statuses.XMLName.Local != "InputProxyChannelStatusList" {
		return Discovery{}, fmt.Errorf("unexpected channel status list")
	}
	byID := map[int]DiscoveredChannel{}
	for _, n := range names.Items {
		if n.ID > 0 {
			byID[n.ID] = DiscoveredChannel{LogicalID: n.ID, StreamID: strconv.Itoa(n.ID*100 + 1), Name: strings.TrimSpace(n.Name)}
		}
	}
	for _, st := range statuses.Items {
		if st.ID <= 0 {
			continue
		}
		ch, ok := byID[st.ID]
		if !ok {
			ch = DiscoveredChannel{LogicalID: st.ID, StreamID: strconv.Itoa(st.ID*100 + 1)}
		}
		for _, id := range st.Streams {
			if id > 0 {
				ch.StreamID = strconv.Itoa(id)
				break
			}
		}
		switch strings.ToLower(strings.TrimSpace(st.Online)) {
		case "true", "1":
			v := true
			ch.Online = &v
		case "false", "0":
			v := false
			ch.Online = &v
		}
		byID[st.ID] = ch
	}
	for _, ch := range byID {
		result.Channels = append(result.Channels, ch)
	}
	sortChannels(result.Channels)
	return result, nil
}

func sortChannels(channels []DiscoveredChannel) {
	sort.Slice(channels, func(i, j int) bool { return channels[i].LogicalID < channels[j].LogicalID })
}
