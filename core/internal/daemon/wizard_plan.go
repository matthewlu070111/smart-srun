package daemon

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

type WifiSetupParams struct {
	Job        string `json:"job"`
	Session    string `json:"session,omitempty"`
	SSID       string `json:"ssid"`
	Key        string `json:"key,omitempty"`
	Encryption string `json:"encryption,omitempty"`
	Iface      string `json:"iface,omitempty"`
	Radio      string `json:"radio,omitempty"`
}

type wizardConnection struct {
	Iface, Radio, Section, SSID, Encryption, Key string
	Reused                                       bool
}

func validateWifiSetup(p WifiSetupParams) error {
	if len(p.Job) != 32 || strings.Trim(p.Job, "0123456789abcdef") != "" {
		return domain.Errorf(domain.CodeInvalidArgument, "无线任务编号无效")
	}
	if p.SSID == "" || len(p.SSID) > 32 || strings.ContainsAny(p.SSID+p.Key, "\x00\r\n") || len(p.Key) > 64 {
		return domain.Errorf(domain.CodeInvalidArgument, "无线名称或密码格式无效")
	}
	if p.Radio != "" && !openwrt.IsLogicalInterfaceName(p.Radio) || p.Iface != "" && !openwrt.IsLogicalInterfaceName(p.Iface) {
		return domain.Errorf(domain.CodeInvalidArgument, "无线电或接口名称无效")
	}
	if !slices.Contains([]string{"", "auto", "none", "psk", "psk2", "psk-mixed", "sae", "sae-mixed"}, p.Encryption) {
		return domain.Errorf(domain.CodeUnsupportedCapability, "向导仅支持开放网络及 WPA/WPA2/WPA3 个人网络")
	}
	return nil
}

func setupKeyValid(encryption, key string) bool {
	if encryption == "none" {
		return key == ""
	}
	if encryption == "sae" {
		return len(key) >= 1 && len(key) <= 63
	}
	if len(key) >= 8 && len(key) <= 63 {
		return true
	}
	if len(key) == 64 && encryption != "sae-mixed" {
		_, err := hex.DecodeString(key)
		return err == nil
	}
	return false
}

func scanEncryption(p WifiSetupParams, found openwrt.ScanResult) string {
	offered := wifi.ClassifyScan(found.Encryption.Enabled, found.Encryption.Authentication, found.Encryption.WPA)
	if p.Encryption != "" && p.Encryption != "auto" {
		if !wifi.ParseSecurity(p.Encryption).Accepts(offered) {
			return ""
		}
		// PSK's security class spans WPA1/2; an explicitly chosen version must
		// actually be advertised by this AP.
		if p.Encryption == "psk2" && !slices.Contains(found.Encryption.WPA, 2) || p.Encryption == "psk" && !slices.Contains(found.Encryption.WPA, 1) {
			return ""
		}
		return p.Encryption
	}
	if p.Key == "" && offered == wifi.SecurityOpen && !found.Encryption.Enabled {
		return "none"
	}
	if p.Key != "" {
		if offered.Accepts(wifi.SecuritySAE) {
			return "sae"
		}
		if offered.Accepts(wifi.SecurityPSK) {
			if slices.Contains(found.Encryption.WPA, 2) {
				return "psk2"
			}
			if slices.Contains(found.Encryption.WPA, 1) {
				return "psk"
			}
		}
	}
	return ""
}

// wizardPlan never rewrites an AP, LAN, or an unrelated enabled station. It
// first reuses a verified existing association, then scans eligible radios.
func (w *deviceWireless) wizardPlan(ctx context.Context, p WifiSetupParams) (wizardConnection, []wireless.PackagePlan, error) {
	if err := validateWifiSetup(p); err != nil {
		return wizardConnection{}, nil, err
	}
	uc, err := w.adapter.UCI(ctx, "wireless")
	if err != nil {
		return wizardConnection{}, nil, err
	}
	radios, err := w.adapter.WirelessStatus(ctx)
	if err != nil {
		return wizardConnection{}, nil, err
	}
	var reusable []wizardConnection
	for _, station := range openwrt.Stations(uc) {
		section, _ := uc.Section(station.Section)
		if station.Disabled || station.SSID != p.SSID || p.Radio != "" && p.Radio != station.Radio || p.Iface != "" && p.Iface != station.Network {
			continue
		}
		encryption := station.Encryption
		if encryption == "" {
			encryption = "none"
		}
		if p.Encryption != "" && p.Encryption != "auto" && p.Encryption != encryption || section.Get("key") != p.Key {
			continue
		}
		connection := wizardConnection{Iface: station.Network, Radio: station.Radio, Section: station.Section, SSID: p.SSID, Encryption: encryption, Key: p.Key, Reused: true}
		if w.wizardAssociated(ctx, connection) {
			reusable = append(reusable, connection)
		}
	}
	if len(reusable) == 1 {
		return reusable[0], nil, nil
	}
	if len(reusable) > 1 {
		return wizardConnection{}, nil, domain.Errorf(domain.CodeConflict, "多个无线出口已连接同名网络，请明确选择无线电和接口")
	}

	iface := p.Iface
	if iface == "" {
		iface = w.settings.Snapshot().STAIface
	}
	if iface == "" {
		iface = "wwan"
	}
	if !openwrt.IsLogicalInterfaceName(iface) || slices.Contains([]string{"lan", "wan", "wan6", "loopback"}, iface) {
		return wizardConnection{}, nil, domain.Errorf(domain.CodeConflict, "临时无线连接需要独立的 DHCP 客户端接口")
	}
	for _, account := range w.settings.Snapshot().CampusAccounts {
		if account.IsWired() && account.WiredIface == iface {
			return wizardConnection{}, nil, domain.Errorf(domain.CodeConflict, "所选接口已用于有线账号")
		}
	}
	type choice struct {
		connection wizardConnection
		signal     int
		bssid      string
	}
	var choices []choice
	sawOpen, sawProtected := false, false
	for _, radio := range openwrt.Radios(uc) {
		if p.Radio != "" && p.Radio != radio.Name {
			continue
		}
		sectionName := stationSection(radio.Name)
		if existing, ok := uc.Section(sectionName); ok && (existing.Type != openwrt.SectionWifiIface || existing.Get("mode") != "sta" || existing.Get(openwrt.ManagedMarker) != "1") {
			continue
		}
		busy := false
		for _, station := range openwrt.Stations(uc) {
			if !station.Disabled && (station.Radio == radio.Name || station.Network == iface) && !(station.Section == sectionName && station.Managed && station.SSID == p.SSID) {
				busy = true
			}
		}
		if busy {
			continue
		}
		live, ok := openwrt.FindRadio(radios, radio.Name)
		if !ok || live.Disabled || live.Pending {
			continue
		}
		device, ok := live.AnyDevice()
		if !ok {
			continue
		}
		results, err := w.adapter.Scan(ctx, device)
		if err != nil {
			continue
		}
		for _, found := range results {
			if found.SSID != p.SSID || !found.Joinable() {
				continue
			}
			if found.Encryption.Enabled {
				sawProtected = true
			} else {
				sawOpen = true
			}
			encryption := scanEncryption(p, found)
			if encryption == "" || !setupKeyValid(encryption, p.Key) {
				continue
			}
			choices = append(choices, choice{wizardConnection{Iface: iface, Radio: radio.Name, Section: sectionName, SSID: p.SSID, Encryption: encryption, Key: p.Key}, found.Signal, found.BSSID})
		}
	}
	if (p.Encryption == "" || p.Encryption == "auto") && p.Key == "" && sawOpen && sawProtected {
		return wizardConnection{}, nil, domain.Errorf(domain.CodeConflict, "发现同名开放和加密网络，请明确选择加密方式")
	}
	if len(choices) == 0 {
		return wizardConnection{}, nil, domain.Errorf(domain.CodeNotFound, "没有可安全连接的匹配网络，请检查无线电、加密方式和密码；不会替换其他活动连接")
	}
	slices.SortStableFunc(choices, func(a, b choice) int {
		if a.signal != b.signal {
			return b.signal - a.signal
		}
		return strings.Compare(a.connection.Radio+a.bssid, b.connection.Radio+b.bssid)
	})
	connection := choices[0].connection
	network, err := w.adapter.UCI(ctx, "network")
	if err != nil {
		return wizardConnection{}, nil, err
	}
	interfaceSection, exists := network.Section(iface)
	if exists && (interfaceSection.Type != "interface" || interfaceSection.Get("proto") != "dhcp" || interfaceSection.Get("device") != "" || interfaceSection.Get("ifname") != "") {
		return wizardConnection{}, nil, domain.Errorf(domain.CodeConflict, "所选接口已有其他网络配置，未予修改")
	}
	for _, section := range uc.SectionsOfType(openwrt.SectionWifiIface) {
		if section.Name != connection.Section && slices.Contains(strings.Fields(section.Get("network")), iface) {
			return wizardConnection{}, nil, domain.Errorf(domain.CodeConflict, "所选接口已被其他无线配置使用")
		}
	}
	firewall, err := w.adapter.UCIExport(ctx, "firewall")
	if err != nil {
		return wizardConnection{}, nil, err
	}
	fwChanges, err := wizardFirewall(firewall, iface)
	if err != nil {
		return wizardConnection{}, nil, err
	}
	var plans []wireless.PackagePlan
	if !exists {
		plans = append(plans, wireless.PackagePlan{Package: "network", Changes: []wireless.Change{
			{Key: wireless.Key{Section: iface}, Text: "interface"}, {Key: wireless.Key{Section: iface, Option: "proto"}, Text: "dhcp"},
		}})
	}
	if len(fwChanges) > 0 {
		plans = append(plans, wireless.PackagePlan{Package: "firewall", Changes: fwChanges})
	}
	section := connection.Section
	changes := []wireless.Change{{Key: wireless.Key{Section: section}, Text: openwrt.SectionWifiIface}}
	for _, pair := range [][2]string{{"device", connection.Radio}, {"mode", "sta"}, {"network", iface}, {"ssid", connection.SSID}, {"encryption", connection.Encryption}, {"disabled", "0"}, {openwrt.ManagedMarker, "1"}, {openwrt.APSelectionOption, "auto"}} {
		changes = append(changes, wireless.Change{Key: wireless.Key{Section: section, Option: pair[0]}, Text: pair[1]})
	}
	changes = append(changes, wireless.Change{Key: wireless.Key{Section: section, Option: "bssid"}, Delete: true}, wireless.Change{Key: wireless.Key{Section: section, Option: "key"}, Text: connection.Key, Delete: connection.Key == ""})
	plans = append(plans, wireless.PackagePlan{Package: "wireless", Changes: changes})
	return connection, plans, nil
}

func wizardFirewall(cfg openwrt.UCIConfig, iface string) ([]wireless.Change, error) {
	var wan []openwrt.UCISection
	attached := false
	for _, zone := range cfg.SectionsOfType("zone") {
		if zone.Get("name") == "wan" {
			wan = append(wan, zone)
		}
		value, _ := zone.Lookup("network")
		networks := strings.Fields(value.Text)
		if value.IsList {
			networks = value.List
		}
		if slices.Contains(networks, iface) {
			if zone.Get("name") != "wan" {
				return nil, domain.Errorf(domain.CodeConflict, "无线接口已属于其他防火墙区域")
			}
			attached = true
		}
	}
	forwarded := false
	for _, rule := range cfg.SectionsOfType("forwarding") {
		if rule.Get("src") == "lan" && rule.Get("dest") == "wan" && rule.Get("enabled") != "0" {
			forwarded = true
		}
	}
	if len(wan) != 1 || wan[0].Get("input") != "REJECT" || wan[0].Get("masq") != "1" || !forwarded {
		return nil, domain.Errorf(domain.CodeConflict, "现有 WAN 防火墙不满足临时连接条件，请先检查 WAN 隔离、转发和地址转换设置")
	}
	if attached {
		return nil, nil
	}
	value, _ := wan[0].Lookup("network")
	members := strings.Fields(value.Text)
	if value.IsList {
		members = slices.Clone(value.List)
	}
	members = append(members, iface)
	return []wireless.Change{wireless.ListChange(wireless.Key{Section: wan[0].Name, Option: "network"}, members...)}, nil
}

func (w *deviceWireless) wizardAssociated(ctx context.Context, c wizardConnection) bool {
	radios, err := w.adapter.WirelessStatus(ctx)
	if err != nil {
		return false
	}
	radio, ok := openwrt.FindRadio(radios, c.Radio)
	if !ok {
		return false
	}
	station, ok := radio.FindInterface(c.Section)
	if !ok || !station.Station() || !slices.Contains(station.Network, c.Iface) || station.IfName == "" {
		return false
	}
	info, err := w.adapter.RadioInfo(ctx, station.IfName)
	if err != nil || !info.Associated() || info.SSID != c.SSID || info.Encrypted != (c.Encryption != "none") {
		return false
	}
	status, err := w.adapter.InterfaceStatus(ctx, c.Iface)
	return err == nil && status.Up && len(status.IPv4) > 0 && status.L3Device == station.IfName
}
