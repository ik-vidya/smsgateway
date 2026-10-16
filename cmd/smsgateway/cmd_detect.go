// Команда smsgateway, файл cmd_detect.go — подкоманда detect-modem.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/modem"
)

func cmdDetectModem() *cobra.Command {
	var (
		asJSON      bool
		skipAT      bool
		atTimeout   time.Duration
		writeConfig string
		portFlag    string
	)

	c := &cobra.Command{
		Use:   "detect-modem",
		Short: "Найти подключённые модемы и показать их параметры",
		Long: `Сканирует последовательные порты, читает метаданные USB из /sys,
стабильные пути из /dev/serial/by-id и /dev/serial/by-path,
опрашивает модем командами AT, AT+CGMI, AT+CGMM, AT+CGMR, AT+CGSN.

Колонка STABLE-ID — ключевая для запуска нескольких инстансов
smsgateway на одной машине: /dev/ttyUSB* переезжает между
перезагрузками, а by-id остаётся привязан к конкретному модему.

С --skip-at порт не открывается: полезно, если модем уже занят
запущенным сервисом. В этом случае AT-поля пусты.

С --write-config <file> команда запишет найденный порт в .env
(сохранив комментарии и остальные переменные). Если модемов
несколько, будет предложено выбрать интерактивно либо указать
--port явно.`,

		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDetectModem(asJSON, skipAT, atTimeout,
				writeConfig, portFlag)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false,
		"вывести JSON вместо таблицы")
	c.Flags().BoolVar(&skipAT, "skip-at", false,
		"не опрашивать модем (только порты и метаданные)")
	c.Flags().DurationVar(&atTimeout, "at-timeout", 2*time.Second,
		"таймаут одной AT-команды")
	c.Flags().StringVar(&writeConfig, "write-config", "",
		"записать SERIAL_PORT в указанный .env и выйти")
	c.Flags().StringVar(&portFlag, "port", "",
		"какой порт использовать при --write-config "+
			"(по умолчанию — единственный найденный, при "+
			"нескольких — интерактивный выбор)")
	return c
}

func runDetectModem(asJSON, skipAT bool, atTimeout time.Duration,
	writeConfig, portFlag string) error {

	ctx := context.Background()

	infos, err := modem.DiscoverPorts(ctx, modem.DiscoverConfig{
		SkipAT:    skipAT,
		ATTimeout: atTimeout,
	})
	if err != nil {
		return err
	}

	if writeConfig != "" {
		return writeConfigFromDetect(writeConfig, portFlag, infos)
	}

	if len(infos) == 0 {
		if asJSON {
			fmt.Println("[]")
		} else {
			fmt.Println("Последовательные порты не найдены.")
		}
		return nil
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(infos)
	}

	printPortTable(infos, skipAT)
	return nil
}

// writeConfigFromDetect выбирает порт и пишет SERIAL_PORT в .env.
func writeConfigFromDetect(path, portFlag string,
	infos []modem.PortInfo) error {

	if len(infos) == 0 {
		return errors.New(
			"последовательные порты не найдены — подключать нечего")
	}

	chosen, err := choosePort(infos, portFlag)
	if err != nil {
		return err
	}

	value := bestPathFor(chosen)
	applied, err := config.UpdateEnvFile(path,
		map[string]string{"SERIAL_PORT": value})
	if err != nil {
		return err
	}

	if len(applied) == 0 {
		fmt.Printf("%s уже содержит SERIAL_PORT=%s\n", path, value)
		return nil
	}

	fmt.Printf("Записано в %s:\n", path)
	for _, line := range applied {
		fmt.Printf("  %s\n", line)
	}
	if chosen.StablePath != "" {
		fmt.Printf("  (стабильный путь by-id: не переезжает между перезагрузками)\n")
	}
	return nil
}

// choosePort выбирает PortInfo из списка.
//
// Правила:
//   - если задан portFlag — ищем точное совпадение по Port,
//     StablePath или ByPathPath;
//   - если один модем — берём его без вопросов;
//   - если несколько и stdin — терминал, показываем нумерованный
//     список и читаем выбор;
//   - иначе — ошибка со списком.
func choosePort(infos []modem.PortInfo, portFlag string) (modem.PortInfo, error) {
	if portFlag != "" {
		for _, p := range infos {
			if p.Port == portFlag ||
				p.StablePath == portFlag ||
				p.ByPathPath == portFlag {
				return p, nil
			}
		}
		return modem.PortInfo{}, fmt.Errorf(
			"порт %q не найден среди обнаруженных", portFlag)
	}
	if len(infos) == 1 {
		return infos[0], nil
	}
	return promptChoice(infos)
}

// promptChoice показывает список портов и читает выбор из stdin.
func promptChoice(infos []modem.PortInfo) (modem.PortInfo, error) {
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return modem.PortInfo{}, fmt.Errorf(
			"найдено %d модемов; укажите --port явно", len(infos))
	}

	fmt.Fprintln(os.Stderr, "Найдено несколько модемов:")
	for i, p := range infos {
		hint := p.StablePath
		if hint == "" {
			hint = p.ByPathPath
		}
		if hint == "" {
			hint = p.Port
		}
		model := p.ModemModel
		if model == "" {
			model = "-"
		}
		fmt.Fprintf(os.Stderr, "  [%d] %s  (%s)  %s\n",
			i+1, p.Port, hint, model)
	}
	fmt.Fprintf(os.Stderr, "Выберите номер [1]: ")

	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return modem.PortInfo{}, fmt.Errorf(
			"не удалось прочитать выбор из stdin")
	}
	s := strings.TrimSpace(sc.Text())
	n := 1
	if s != "" {
		var err error
		n, err = strconv.Atoi(s)
		if err != nil || n < 1 || n > len(infos) {
			return modem.PortInfo{}, fmt.Errorf(
				"некорректный выбор %q (ожидалось 1..%d)", s, len(infos))
		}
	}
	return infos[n-1], nil
}

// bestPathFor выбирает, что писать в SERIAL_PORT.
//
// Приоритет: by-id → by-path → обычный путь.
// by-id стабилен между перезагрузками и переподключениями,
// by-path — между переназначениями нумерации ttyUSB*,
// /dev/ttyUSB* — крайний случай.
func bestPathFor(p modem.PortInfo) string {
	switch {
	case p.StablePath != "":
		return p.StablePath
	case p.ByPathPath != "":
		return p.ByPathPath
	default:
		return p.Port
	}
}

func printPortTable(infos []modem.PortInfo, skipAT bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if skipAT {
		fmt.Fprintln(w, "PORT\tSTABLE-ID\tVID:PID\tSERIAL\tPRODUCT")
	} else {
		fmt.Fprintln(w, "PORT\tSTABLE-ID\tVID:PID\tIMEI\tMODEL")
	}
	for _, p := range infos {
		vidpid := ""
		if p.VID != "" || p.PID != "" {
			vidpid = strings.ToUpper(p.VID + ":" + p.PID)
		}
		stable := p.StablePath
		if stable == "" {
			stable = p.ByPathPath
		}
		if stable == "" {
			stable = "-"
		}

		if skipAT {
			serial := p.SerialNumber
			if serial == "" {
				serial = "-"
			}
			product := p.Product
			if product == "" {
				product = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				p.Port, stable, orDash(vidpid), serial, product)
		} else {
			model := p.ModemModel
			if model == "" {
				model = p.ATError
			}
			if model == "" {
				model = "-"
			}
			imei := p.IMEI
			if imei == "" {
				imei = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				p.Port, stable, orDash(vidpid), imei, model)
		}
	}
	_ = w.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
