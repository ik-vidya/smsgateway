// Команда smsgateway, файл cmd_convert.go — подкоманда
// convert-gammu-config.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ik-vidya/smsgateway/internal/gammu"
)

func cmdConvertGammuConfig() *cobra.Command {
	var (
		from    string
		to      string
		verbose bool
	)

	c := &cobra.Command{
		Use:   "convert-gammu-config",
		Short: "Конвертировать gammu-smsdrc в .env для smsgateway",
		Long: `Читает файл конфигурации gammu-smsd (обычно /etc/gammu-smsdrc)
и генерирует фрагмент .env для smsgateway.

Конвертируются только совместимые опции: Connection/Device, Pin,
Service, InboxPath/OutboxPath/SentSMSPath/ErrorSMSPath,
InboxFormat, MaxRetries. Остальные опции перечисляются в отчёте.

Поддерживается только Connection = serial или at: irda,
bluetooth и другие транспорты не поддерживаются.

Фрагмент не является полным .env: в нём нет API_KEY, DB_PATH,
HTTP_ADDR и прочих обязательных параметров — их нужно
добавить вручную.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runConvertGammuConfig(from, to, verbose)
		},
	}
	c.Flags().StringVar(&from, "from", "",
		"путь к gammu-smsdrc (обязателен)")
	c.Flags().StringVar(&to, "to", "",
		"куда записать .env; без флага — в stdout")
	c.Flags().BoolVar(&verbose, "verbose", false,
		"печатать полный отчёт об игнорированных опциях")
	_ = c.MarkFlagRequired("from")
	return c
}

func runConvertGammuConfig(from, to string, verbose bool) error {
	if from == "" {
		return fmt.Errorf("флаг --from обязателен")
	}

	cfg, err := gammu.LoadSMSDConfig(from)
	if err != nil {
		return fmt.Errorf("чтение %q: %w", from, err)
	}
	res, err := gammu.ConvertSMSDConfig(cfg)
	if err != nil {
		return err
	}

	out := res.RenderEnv(from)

	if to == "" {
		fmt.Print(out)
	} else {
		if err := os.WriteFile(to, []byte(out), 0o644); err != nil {
			return fmt.Errorf("запись %q: %w", to, err)
		}
		fmt.Printf("Записано в %s\n", to)
	}

	// Отчёт.
	printConvertReport(res, verbose)
	return nil
}

func printConvertReport(res *gammu.ConvertResult, verbose bool) {
	fmt.Fprintln(os.Stderr)

	if len(res.Warnings) > 0 {
		fmt.Fprintln(os.Stderr, "Предупреждения:")
		for _, w := range res.Warnings {
			fmt.Fprintf(os.Stderr, "  ! %s\n", w)
		}
	}

	if len(res.Ignored) > 0 {
		fmt.Fprintf(os.Stderr, "\nПроигнорировано опций: %d", len(res.Ignored))
		if !verbose {
			fmt.Fprintln(os.Stderr, " (--verbose для списка)")
		} else {
			fmt.Fprintln(os.Stderr)
			sort.Slice(res.Ignored, func(i, j int) bool {
				a, b := res.Ignored[i], res.Ignored[j]
				if a.Section != b.Section {
					return a.Section < b.Section
				}
				return a.Key < b.Key
			})
			for _, o := range res.Ignored {
				fmt.Fprintf(os.Stderr, "  - %s\n", o.String())
			}
		}
	}

	if len(res.Unparsed) > 0 {
		fmt.Fprintf(os.Stderr, "\nНеизвестных секций: %d\n", len(res.Unparsed))
		if verbose {
			for _, s := range res.Unparsed {
				fmt.Fprintf(os.Stderr, "  ? %s\n", s)
			}
		}
	}

	// Что сконвертировано.
	if len(res.Env) > 0 {
		keys := make([]string, 0, len(res.Env))
		for k := range res.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(os.Stderr,
			"\nСконвертировано ключей: %d (%s)\n",
			len(keys), strings.Join(keys, ", "))
	}
}
