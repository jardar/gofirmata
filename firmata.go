/*
  Package: Firmata

  This is a binding for the arduino Firmata package.

  You need to burn the simple Firmata image onto your Arduino then you 
  can control it over the USB using this library.

  import "github.com/choffee/gofirmata"

  func main () {

    board, err := NewBoard("/dev/ttyUSB1", 57600)
    if err != nil {
      log.Fatal("Could not setup board")
    }
    // Set the mode of a pin
    println("set 13 to output")
    board.SetPinMode(13,MODE_OUTPUT)

    // Turn on pin 13
    println("set 13 to 1")
    board.WriteDigital(13,1)
  }

*/
package firmata

import (
	"fmt"
	"github.com/tarm/goserial"
	"io"
	"log"
	"strconv"
	"time"
)

const (
	MODE_INPUT  byte = 0x00
	MODE_OUTPUT byte = 0x01
	MODE_ANALOG byte = 0x02
	MODE_PWM    byte = 0x03
	MODE_SERVO  byte = 0x04
	MODE_SHIFT  byte = 0x05
	MODE_I2C    byte = 0x06

	I2C_MODE_WRITE           byte = 0x00
	I2C_MODE_READ            byte = 0x01
	I2C_MODE_CONTINIOUS_READ byte = 0x02
	I2C_MODE_STOP_READING    byte = 0x03

	HIGH byte = 1
	LOW  byte = 0

	UNKNOWN                 byte = 0xFF // I just invented this it could be used elsewhere
	START_SYSEX             byte = 0xF0 // start a MIDI Sysex message
	END_SYSEX               byte = 0xF7 // end a MIDI Sysex message
	PIN_MODE_QUERY          byte = 0x72 // ask for current and supported pin modes
	PIN_MODE_RESPONSE       byte = 0x73 // reply with current and supported pin modes
	PIN_STATE_QUERY         byte = 0x6D
	PIN_STATE_RESPONSE      byte = 0x6E
	CAPABILITY_QUERY        byte = 0x6B
	CAPABILITY_RESPONSE     byte = 0x6C
	ANALOG_MAPPING_QUERY    byte = 0x69
	ANALOG_MAPPING_RESPONSE byte = 0x6A
	REPORT_FIRMWARE         byte = 0x79 // report name and version of the firmware
	PIN_MODE                byte = 0xF4 // Set the pin mode
	ANALOG_MESSAGE          byte = 0xE0
	I2C_REQUEST             byte = 0x76
	I2C_REPLY               byte = 0x77
	I2C_CONFIG              byte = 0x78
	SAMPLING_INTERVAL       byte = 0x7A

	DIGITAL_WRITE byte = 0x90
	DIGITAL_READ  byte = 0xD0
	ANALOG_WRITE  byte = 0xE0
	ANALOG_READ   byte = 0xC0
	PROTOCOL_VER  byte = 0xF9
)

type FirmataMsg struct {
	msgtype byte
	pin     byte
	data    map[string]string
	rawdata []byte
}

type pinmode struct {
	mode       byte
	resolution byte
}

type pinCapability struct {
	modes []pinmode
}

type Board struct {
	Name            string
	config          *serial.Config
	device          string
	Debug           int // 0 no debug
	baud            int
	serial          io.ReadWriteCloser
	Reader          chan FirmataMsg
	Writer          chan FirmataMsg
	digitalPins     [8]byte  // Keeps a record of digital pin values
	analogPins      [16]byte // Keeps a record of analog pin values
	pinCapabilities []pinCapability
	analogMappings  []byte // one for each pin showing mapped analog pin
	version         map[string]byte
}

// Setup the board to start reading and writing
// It needs a device in the format "/dev/ttyUSB0"
// and a baud rate eg. 57600
func NewBoard(device string, baud int) (*Board, error) {
	board := new(Board)
	board.device = device
	board.baud = baud
	board.config = &serial.Config{Name: board.device, Baud: board.baud}
	var err error
	board.serial, err = serial.OpenPort(board.config)
	if err != nil {
		log.Fatal("Could not open port")
		return board, err
	}
	board.GetReader()
	board.GetCapabilities()
	board.GetAnalogMapping()
	return board, err
}

// Return the Device  the board is using
func (board *Board) Device() string {
	return board.device
}

// Return the baud rate the device is using
func (board *Board) Baud() int {
	return board.baud
}

func (board *Board) process_sysex(msgdata []byte) FirmataMsg {
	var result FirmataMsg
	result.rawdata = msgdata
	result.msgtype = msgdata[0]
	fmt.Println(msgdata)
	switch msgdata[0] {
	case REPORT_FIRMWARE: // queryFirmware
		result.data = make(map[string]string)
		result.data["major"] = strconv.Itoa(int(msgdata[1]))
		result.data["minor"] = strconv.Itoa(int(msgdata[2]))
		result.data["name"] = string(msgdata[3:]) //TODO This needs to converted from 7bit
	case CAPABILITY_RESPONSE:
		var mode pinmode
		var capa []pinmode
		pin := 0
		for i := 1; i < len(msgdata); i = i + 2 {
			if msgdata[i] == 127 {
				board.pinCapabilities[pin].modes = capa
				pin++
				capa = nil
			}
			mode.mode = msgdata[i]
			mode.resolution = msgdata[i+1]
			capa = append(capa, mode)
		}
	case ANALOG_MAPPING_RESPONSE:
		// discard the sysex type then map each pin
		for pin, level := range msgdata[1:] {
			board.analogMappings[pin] = level
		}
	case PIN_STATE_RESPONSE:
		result.pin = msgdata[1]
		result.data["mode"] = string(msgdata[2])
		state := 0
		for mult, st := range msgdata[3:] {
			state = state + int(st<<(7*uint(mult)))
		}
		result.data["state"] = string(state)
	case I2C_REPLY:
		result.data["address"] = string(toInt7(msgdata[1], msgdata[2]))
		result.data["register"] = string(toInt7(msgdata[3], msgdata[4]))
		data := ""
		for f := 5; f < len(msgdata); f = f + 2 {
			data = data + string(toInt7(msgdata[f], msgdata[f+1]))
		}
		result.data["i2cdata"] = data
	default:
		result.msgtype = UNKNOWN
		result.data = make(map[string]string)
		result.data["msgtyperaw"] = string(msgdata[0])
		result.data["unknown"] = string(msgdata)
	}
	return result
}

func toInt7(lsb, msb byte) int {
	return int(lsb + (msb << 7))
}

func (board *Board) processMIDI(cmd, first byte) FirmataMsg {
	var msg FirmataMsg
	m := make([]byte, 2)
	var err error
	_, err = board.serial.Read(m)
	if err != nil {
		log.Fatal("Failed to read the rest of the MIDI message")
	}
	msg.msgtype = cmd
	switch cmd {
	case ANALOG_MESSAGE:
		pin := first & 0x0F
		msg.pin = pin
		value := m[0] | m[1]<<7
		msg.data = map[string]string{"value": fmt.Sprintf("%x", value)}
		board.analogPins[pin] = value
	case PROTOCOL_VER:
		board.version = map[string]byte{"major": m[0], "minor": m[1]}
		msg.data = map[string]string{
			"major_ver": fmt.Sprintf("%x", m[0]),
			"minor_ver": fmt.Sprintf("%x", m[1]),
		}
	}
	return msg
}

// Show the board version
func (board *Board) Version() map[string]byte {
	version := board.version
	return version
}

// Sets up the reader channel
// You can then fetch read events from  <- board.Reader
func (board *Board) GetReader() {
	board.Reader = make(chan FirmataMsg)
	// Sleep for a bit before we start to read
	time.Sleep(1000 * time.Millisecond)
	go func() {
		var err error
		l := make([]byte, 1)
		for _, err = board.serial.Read(l); ; _, err = board.serial.Read(l) {
			if err != nil {
				print("Read Err: ")
				fmt.Println(err)
				log.Fatal("Failed to read from Serial port")
				return
			}
			switch l[0] {
			case START_SYSEX:
				var msgdata []byte
				for m := make([]byte, 1); m[0] != END_SYSEX; _, err = board.serial.Read(m) {
					if err != nil {
						log.Fatal("Failed to read sysex from serial port")
					} else {
						msgdata = append(msgdata, m[0])
					}
				}
				// Send the message down the chanel
				newmsg := board.process_sysex(msgdata)
				if board.Debug > 9 {
					log.Printf("Sysex Rec: %v", newmsg)
				}
				board.Reader <- newmsg
			default:
				var cmd byte
				if l[0] < 240 {
					cmd = l[0] & 0xF0
				} else {
					cmd = l[0]
				}
				if board.Debug > 9 {
					log.Printf("Midi Rec: %v", cmd)
				}
				newmsg := board.processMIDI(cmd, l[0])
				board.Reader <- newmsg
			}
		}
	}()
}

// Expects the sysex message and just wraps it
// in sysex start/end then sends it
func (board *Board) sendSysex(msg []byte) {
	sysex := make([]byte, len(msg)+2)
	sysex[0] = START_SYSEX
	copy(sysex[1:], msg)
	sysex[len(msg)+1] = END_SYSEX
	board.sendRaw(&sysex)
	if board.Debug > 0 {
		fmt.Println(&sysex)
	}
}

func (board *Board) sendRaw(msg *[]byte) {
	board.serial.Write(*msg)
	if board.Debug > 9 {
		log.Printf("RawMsg: %v\n", &msg)
	}
}

func (board *Board) GetAnalogMapping() {
	msg := []byte{ANALOG_MAPPING_QUERY}
	board.sendSysex(msg)
}

func (board *Board) GetCapabilities() {
	msg := []byte{CAPABILITY_QUERY}
	board.sendSysex(msg)
}

// Set the mode for a pin
// mode should be one of: MODE_INPUT MODE_OUTPUT, MODE_ANALOG,
//                        MODE_PWM, MODE_SERVO, MODE_SHIFT, MODE_I2C
func (board *Board) SetPinMode(pin, mode byte) {
	cmd := byte(PIN_MODE)
	msg := []byte{cmd, pin & 0x7F, mode & 0x7F}
	board.sendRaw(&msg)

}

// Convert a digital pin to port
func pin2port(pin byte) byte {
	return (pin >> 3) & 0x0F // Get the port the pin is in
}

// Write a value to a pin
// value should be firmata.HIGH or firmata.LOW
func (board *Board) WriteDigital(pin, value byte) {
	port := pin2port(pin)
	// Next we need to get all 8 pins for that port and only change the one
	// we are intrested in
	switch value {
	case 0:
		board.digitalPins[port] = board.digitalPins[port] & ^(1 << (pin & 0x07))
	case 1:
		board.digitalPins[port] = (board.digitalPins[port] | (1 << (pin & 0x07)))
	}
	// Now send the whole port ( 8 pins ) to the arduino
	cmd := byte(DIGITAL_WRITE | port)
	msg := []byte{cmd, board.digitalPins[port] & 0x7F, (board.digitalPins[port] >> 7) & 0x7f}
	board.sendRaw(&msg)
}

// Write an analog value to a pin
func (board *Board) WriteAnalog(pin, value byte) {
	cmd := byte(ANALOG_WRITE | pin)
	msg := []byte{cmd, value & 0x7F, (value >> 7) & 0x7F}
	board.sendRaw(&msg)
	log.Printf("%v, %v", board.analogPins, pin)
	board.analogPins[pin] = value
}

// Starts and stops the analog pin reporting
// state: 0/1 disable/enable
func (board *Board) SetReadAnalog(pin, state byte) {
	cmd := byte(ANALOG_READ | (pin & 0xF))
	msg := []byte{cmd, state & 0x1}
	board.sendRaw(&msg)
}

// Starts and stops the digital pin reading for a port
// state: 0/1 disable/enable
// ( It start reading for the whole port )
func (board *Board) SetReadDigital(pin, state byte) {
	port := pin2port(pin)
	cmd := byte(DIGITAL_READ | port)
	msg := []byte{cmd, state & 0x1}
	board.sendRaw(&msg)
}

// Send the I2C config command
// Should be run before sending I2C commands
func (board *Board) I2CConfig(delay int) {
	msg := make([]byte, 3)
	msg[0] = I2C_CONFIG
	msg[1] = byte(1) // Power pins on
	msg[1] = byte(delay & 0x7F)
	msg[2] = byte((delay >> 7) & 0x7F)
	board.sendSysex(msg)
}

// Send analog report interval command
// interval (millisecond)
func (board *Board) SetSamplingInterval(interval int) {
	msb := byte((interval << 1) >> 8 & 0x7F)
	lsb := byte(interval & 0x7F)
	msg := []byte{SAMPLING_INTERVAL, lsb, msb}
	board.sendSysex(msg)
}

// Send an I2C message
// addr is the address on the I2C bus to send it too
// msg is a slice containg the message to send
// mode: Should be one of I2C_MODE_WRITE, I2C_MODE_READ, 
//       I2C_MODE_CONTINIOUS_READ or I2C_MODE_STOP_READING
// We are only supporting 7bit addresses
func (board *Board) I2CWrite(addr, mode byte, msg []byte) {
	newLength := len(msg)*2 + 3
	fullmsg := make([]byte, newLength)
	fullmsg[0] = I2C_REQUEST
	fullmsg[1] = addr & 0x7F
	fullmsg[2] = mode << 3
	for l := 0; l < len(msg); l++ {
		fullmsg[3+l*2] = msg[l] & 0x7F
		fullmsg[4+l*2] = msg[l] >> 7 & 0x7F
	}
	board.sendSysex(fullmsg)
}
