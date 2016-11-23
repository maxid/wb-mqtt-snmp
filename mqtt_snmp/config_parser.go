package mqtt_snmp

import (
	"encoding/json"
	"fmt"
	"github.com/alouca/gosnmp"
	"github.com/contactless/wbgo"
	"io"
	"io/ioutil"
	"math"
	"regexp"
	"strconv"
	// "log"
)

const (
	// Default templates directory
	// TemplatesDirectory = "/usr/share/wb-mqtt-snmp/templates"
	TemplatesDirectory = "./templates"

	// Template file regexp
	TemplatesFileMask = "config-.*\\.json"

	// Default poll interval for channels
	DefaultChannelPollInterval = 1000

	// Default channel control type
	DefaultChannelControlType = "value"

	// Default SNMP version
	DefaultSnmpVersion = gosnmp.Version2c

	// Default SNMP timeout (ms)
	DefaultSnmpTimeout = 1000

	floatEps = 0.00001 // epsilon to compare floats
)

// Device templates storage type
type deviceTemplatesStorage struct {
	templates map[string]map[string]interface{}
	Valid     bool
}

// Load template files from directory
func (tpl *deviceTemplatesStorage) Load(dir string) error {

	if tpl.Valid {
		return nil // templates are already loaded
	}

	files, err := ioutil.ReadDir(dir)

	if err != nil {
		return fmt.Errorf("failed to read templates dir %s: %s", dir, err.Error())
	}

	tpl.templates = make(map[string]map[string]interface{})

	for _, file := range files {
		m, err := regexp.MatchString(TemplatesFileMask, file.Name())
		if err != nil {
			return fmt.Errorf("error in filename regexp: %s", err.Error())
		}

		// skip files which don't match regexp
		if !m {
			continue
		}

		data, err := ioutil.ReadFile(dir + "/" + file.Name())

		if err != nil {
			return fmt.Errorf("failed to read template file %s: %s", file.Name(), err.Error())
		}

		var jsonData map[string]interface{}

		if err := json.Unmarshal(data, &jsonData); err != nil {
			return fmt.Errorf("failed to parse JSON in template file %s: %s", file.Name(), err.Error())
		}

		if devTypeEntry, ok := jsonData["device_type"]; ok {
			if devType, valid := devTypeEntry.(string); valid {
				tpl.templates[devType] = jsonData
			} else {
				return fmt.Errorf("template error: device_type must be string in %s", file.Name())
			}
		} else {
			return fmt.Errorf("template error: device_type is not present in %s", file.Name())
		}
	}

	tpl.Valid = true

	return nil
}

// Initialize raw device entry using template
func (tpl *deviceTemplatesStorage) InitEntry(devType string, entry *map[string]interface{}) error {
	if data, ok := tpl.templates[devType]; ok {
		*entry = data
	} else {
		return fmt.Errorf("no such template: %s", devType)
	}

	return nil
}

// Channel value converter type
type ValueConverter func(string) string

func AsIs(s string) string { return s }

func Scale(factor float64) ValueConverter {
	return func(s string) string {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			wbgo.Warn.Printf("can't convert numeric value: %s", s)
			return s
		}

		// skip conversion if scale is 1
		if math.Abs(factor-1.0) < floatEps {
			return s
		}

		return strconv.FormatFloat(f*factor, 'f', 1, 64)
	}
}

// Check if control type is numeric
func isNumericControlType(ctype string) bool {
	return ctype != "text"
}

// Final structures
type ChannelConfig struct {
	Name, Oid, ControlType string
	Conv                   ValueConverter
	PollInterval           int
}

type DeviceConfig struct {
	Name, Id, Address, DeviceType, Community string
	SnmpVersion                              gosnmp.SnmpVersion
	SnmpTimeout                              int

	// Channels is map from channel names
	Channels map[string]ChannelConfig
}

// Whole daemon configuration structure
type DaemonConfig struct {
	Debug     bool
	templates deviceTemplatesStorage

	// Devices storage is map from device IDs
	Devices map[string]DeviceConfig
}

// Load templates from directory into DaemonConfig storage
func (c *DaemonConfig) LoadTemplates(path string) (err error) {
	err = c.templates.Load(path)
	return
}

// Generate daemon config from input stream and directory with templates
//
func NewDaemonConfig(input io.Reader, templatesDir string) (config *DaemonConfig, err error) {
	config = &DaemonConfig{}
	if err = config.LoadTemplates(templatesDir); err != nil {
		return
	}

	err = json.NewDecoder(input).Decode(config)

	return
}

// Make empty device config, fill it with
// default configuration values such as SnmpVersion and SnmpTimeout
func NewEmptyDeviceConfig() DeviceConfig {
	d := DeviceConfig{DeviceType: "", Community: "", SnmpVersion: DefaultSnmpVersion, SnmpTimeout: DefaultSnmpTimeout}
	return d
}

// Make empty channel config
func NewEmptyChannelConfig() ChannelConfig {
	c := ChannelConfig{ControlType: DefaultChannelControlType, Conv: AsIs, PollInterval: DefaultChannelPollInterval}
	return c
}

// JSON unmarshaller for DaemonConfig
func (c *DaemonConfig) UnmarshalJSON(raw []byte) error {
	var root struct {
		Debug   bool
		Devices []map[string]interface{}
	}

	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("can't parse config JSON file: %s", err.Error())
	}

	c.Debug = root.Debug
	c.Devices = make(map[string]DeviceConfig)

	// parse devices config
	return c.parseDevices(root.Devices)
}

// Copy raw interface{} data from map to string
func copyString(fromMap *map[string]interface{}, key string, to *string, required bool) error {
	if entry, ok := (*fromMap)[key]; ok {
		if val, valid := entry.(string); valid {
			*to = val
		} else {
			return fmt.Errorf("%s must be string, but %T given", key, entry)
		}
	} else {
		if required {
			return fmt.Errorf("%s is not present", key)
		}
	}

	return nil
}

// Copy raw interface{} data from map to int
func copyInt(fromMap *map[string]interface{}, key string, to *int, required bool) error {
	if entry, ok := (*fromMap)[key]; ok {
		if val, valid := entry.(float64); valid {
			*to = int(val)
		} else {
			return fmt.Errorf("%s must be int, but %T given", key, entry)
		}
	} else {
		if required {
			return fmt.Errorf("%s is not present", key)
		}
	}

	return nil
}

// Copy raw interface{} data from map to SnmpVersion
func copySnmpVersion(fromMap *map[string]interface{}, key string, to *gosnmp.SnmpVersion, required bool) error {
	if entry, ok := (*fromMap)[key]; ok {
		if val, valid := entry.(string); valid {
			switch val {
			case "1":
				*to = gosnmp.Version1
			case "2c":
				*to = gosnmp.Version2c
			default:
				return fmt.Errorf("SNMP version must be either 1 or 2c, %s given", val)
			}
		} else {
			return fmt.Errorf("%s must be int, but %T given", key, entry)
		}
	} else {
		if required {
			return fmt.Errorf("%s is not present", key)
		}
	}

	return nil
}

// Copy raw interface{} data from map to float64
func copyFloat64(fromMap *map[string]interface{}, key string, to *float64, required bool) error {
	if entry, ok := (*fromMap)[key]; ok {
		if val, valid := entry.(float64); valid {
			*to = val
		} else {
			return fmt.Errorf("%s must be number, but %T given", key, entry)
		}
	} else {
		if required {
			return fmt.Errorf("%s is not present", key)
		}
	}

	return nil
}

// Parse devices list
func (c *DaemonConfig) parseDevices(devs []map[string]interface{}) error {
	// for each element in input slice - create DeviceConfig structure
	for _, value := range devs {
		if err := c.parseDeviceEntry(value); err != nil {
			return err
		}
	}

	return nil
}

// Try to get name from channel entry
func getNameFromEntry(entry *map[string]interface{}) (name string, err error) {
	err = nil

	var valid bool

	if nameEntry, ok := (*entry)["name"]; ok {
		if name, valid = nameEntry.(string); !valid {
			err = fmt.Errorf("channel name must be string, %T given", nameEntry)
		}
	} else {
		err = fmt.Errorf("no channel name present")
	}

	return
}

// Lay real data over device template
func (c *DaemonConfig) layConfigDataOverTemplate(entry *map[string]interface{}, devConfig *map[string]interface{}) error {
	// rewrite all elements except 'channels'
	for key, value := range *devConfig {
		if key != "channels" {
			(*entry)[key] = value
		}
	}

	// merge channels
	// check channels list from template
	var channelsList []interface{}
	var ok, valid bool
	var channelsListEntry, devChannelsListEntry interface{}

	if channelsListEntry, ok = (*entry)["channels"]; ok {
		if channelsList, valid = channelsListEntry.([]interface{}); !valid {
			return fmt.Errorf("channels list must be array of objects; %T given", channelsListEntry)
		}
	}

	// check channels list from device description
	var devChannelsList []interface{}
	if devChannelsListEntry, ok = (*devConfig)["channels"]; ok {
		if devChannelsList, valid = devChannelsListEntry.([]interface{}); !valid {
			return fmt.Errorf("channels list must be array of objects; %T given", devChannelsListEntry)
		}
	}

	// create merging map
	channelsMap := make(map[string]map[string]interface{})

	createMap := func(l *[]interface{}, m *map[string]map[string]interface{}) error {
		for _, chanEntry := range *l {
			if channel, valid := chanEntry.(map[string]interface{}); valid {
				if name, err := getNameFromEntry(&channel); err == nil {
					(*m)[name] = channel
				} else {
					return err
				}
			} else {
				return fmt.Errorf("channel config must be object, %T given", chanEntry)
			}
		}

		return nil
	}

	if err := createMap(&channelsList, &channelsMap); err != nil {
		return err
	}

	// merge devChannelsMap into channelsMap
	for _, chanEntry := range devChannelsList {

		if channel, valid := chanEntry.(map[string]interface{}); valid {
			// get name
			if name, err := getNameFromEntry(&channel); err == nil {
				// check if this name is present in channel map
				if _, present := channelsMap[name]; present {
					// merge entries
					for n, v := range channel {
						channelsMap[name][n] = v
					}
				} else {
					// create new entry
					channelsMap[name] = channel
				}
			} else {
				return err
			}
		} else {
			return fmt.Errorf("channel config must be object, %T given", chanEntry)
		}
	}

	// expose channelsMap to entry
	chanList := make([]map[string]interface{}, len(channelsMap))
	i := 0
	for _, value := range channelsMap {
		chanList[i] = value
		i += 1
	}

	(*entry)["channels"] = chanList

	return nil
}

// Parse single device entry
func (c *DaemonConfig) parseDeviceEntry(devConfig map[string]interface{}) error {

	// Check if device is enabled and skip if not
	if enableEntry, ok := devConfig["enabled"]; ok {
		if enableValue, valid := enableEntry.(bool); valid {
			if !enableValue {
				return nil // device is disabled, nothing to do here
			}
		} else {
			return fmt.Errorf("'enable' must be bool, %T given", enableEntry)
		}
	} // if 'enable' is not presented, think that device is enabled by default

	// Get device type and apply template to it
	var devType string
	devEntry := make(map[string]interface{})
	var valid bool

	// device_type is optional; if not present, just don't apply template
	if devTypeEntry, ok := devConfig["device_type"]; ok {
		if devType, valid = devTypeEntry.(string); valid {
			if err := c.templates.InitEntry(devType, &devEntry); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("device_type must be string, but %T given", devTypeEntry)
		}
	}

	// Lay config data over template
	if err := c.layConfigDataOverTemplate(&devEntry, &devConfig); err != nil {
		return err
	}

	// Parse whole tree
	d := NewEmptyDeviceConfig()

	// insert entries in a hard way
	// address field is required
	if err := copyString(&devEntry, "address", &(d.Address), true); err != nil {
		return err
	}

	// fill default values
	d.Name = "SNMP " + d.Address
	d.Id = "snmp_" + d.Address

	if err := copyString(&devEntry, "name", &(d.Name), false); err != nil {
		return err
	}
	if err := copyString(&devEntry, "id", &(d.Id), false); err != nil {
		return err
	}
	if err := copyString(&devEntry, "device_type", &(d.DeviceType), false); err != nil {
		return err
	}
	if err := copyString(&devEntry, "community", &(d.Community), false); err != nil {
		return err
	}
	if err := copySnmpVersion(&devEntry, "snmp_version", &(d.SnmpVersion), false); err != nil {
		return err
	}
	if err := copyInt(&devEntry, "snmp_timeout", &(d.SnmpTimeout), false); err != nil {
		return err
	}

	d.Channels = make(map[string]ChannelConfig)

	// parse channels
	if channelsEntry, ok := devEntry["channels"]; ok {
		if channels, valid := channelsEntry.([]map[string]interface{}); valid {
			if err := d.parseChannels(channels); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("channels list in %s must be array of objects, %T given", d.Name, channelsEntry)
		}
	} else {
		return fmt.Errorf("channels list is not present for %s", d.Name)
	}

	// append device to storage
	c.Devices[d.Id] = d

	return nil
}

// Parse channels list
func (d *DeviceConfig) parseChannels(chans []map[string]interface{}) error {
	// for each element in input slice - create ChannelConfig structure and append to DeviceConfig
	if len(chans) == 0 {
		return fmt.Errorf("channels list is empty for %s", d.Name)
	}

	for _, value := range chans {
		if err := d.parseChannelEntry(value); err != nil {
			return err
		}
	}

	return nil
}

// Parse single channel entry
func (d *DeviceConfig) parseChannelEntry(channel map[string]interface{}) error {
	// create channel config struct
	c := NewEmptyChannelConfig()

	// fill channel config
	//
	// name is required
	if err := copyString(&channel, "name", &(c.Name), true); err != nil {
		return err
	}

	// oid is required
	if err := copyString(&channel, "oid", &(c.Oid), true); err != nil {
		return err
	}

	// control type is optional
	if err := copyString(&channel, "control_type", &(c.ControlType), false); err != nil {
		return err
	}

	// converter is an optional function depends on control type
	// now scale function is presented only
	if _, ok := channel["scale"]; ok {
		if !isNumericControlType(c.ControlType) {
			return fmt.Errorf("scale could be applied only to numeric control type")
		} else {
			var scale float64
			if err := copyFloat64(&channel, "scale", &scale, false); err != nil {
				return err
			}
			c.Conv = Scale(scale)
		}
	}

	// poll interval is optional
	if err := copyInt(&channel, "poll_interval", &(c.PollInterval), false); err != nil {
		return err
	}

	// append channel config to device
	d.Channels[c.Name] = c

	return nil
}
