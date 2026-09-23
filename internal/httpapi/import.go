package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/electrokomplekt/replenishment/internal/planning"
	"github.com/electrokomplekt/replenishment/internal/workbook"
)

// Import is a read-only preview. The existing revision-checked PUT is the only
// operation that replaces warehouse data, including after Excel conversion.
func (a *API) importXLSX(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		problem(w, 415, "unsupported_media_type", "Для Excel нужен multipart/form-data с полями files, as_of и history_start.")
		return
	}
	// Reject excess work before buffering a potentially large upload.
	select {
	case a.importSlots <- struct{}{}:
		defer func() { <-a.importSlots }()
	default:
		w.Header().Set("Retry-After", "5")
		problem(w, 429, "import_busy", "Сейчас обрабатывается другой Excel-импорт. Повторите через несколько секунд.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		problem(w, 400, "invalid_upload", "Не удалось прочитать загрузку Excel. Выберите файлы снова.")
		return
	}
	files := make([]workbook.File, 0, 12)
	fields := make(map[string]string)
	for {
		if r.Context().Err() != nil {
			return
		}
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			importReadError(w, err)
			return
		}
		name := part.FormName()
		if part.FileName() != "" {
			if name != "files" || !strings.EqualFold(filepath.Ext(part.FileName()), ".xlsx") {
				problem(w, 400, "invalid_upload", "Добавьте только файлы .xlsx в поле files. JSON загружается отдельно.")
				return
			}
			if len(files) == 12 {
				problem(w, 400, "too_many_files", "Можно загрузить не больше 12 Excel-файлов за один раз.")
				return
			}
			data, err := io.ReadAll(part)
			if err != nil {
				importReadError(w, err)
				return
			}
			files = append(files, workbook.File{Name: part.FileName(), Data: data})
		} else {
			if (name != "as_of" && name != "history_start") || fields[name] != "" {
				problem(w, 400, "invalid_upload", "Укажите as_of и history_start ровно по одному разу.")
				return
			}
			data, err := io.ReadAll(io.LimitReader(part, 11))
			if err != nil {
				importReadError(w, err)
				return
			}
			if len(data) != 10 {
				problem(w, 400, "invalid_upload", "Даты as_of и history_start должны иметь формат YYYY-MM-DD.")
				return
			}
			fields[name] = string(data)
		}
		if err := part.Close(); err != nil {
			importReadError(w, err)
			return
		}
	}
	if len(files) == 0 || fields["as_of"] == "" || fields["history_start"] == "" {
		problem(w, 400, "invalid_upload", "Выберите Excel-файлы и укажите дату снимка и начало истории продаж.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	data, err := workbook.Import(ctx, files, workbook.Options{AsOf: fields["as_of"], HistoryStart: fields["history_start"]})
	if err != nil {
		a.logger.Warn("import Excel workbooks", "error", err)
		if ctx.Err() != nil {
			problem(w, 408, "import_timeout", "Время обработки Excel истекло. Повторите загрузку отдельно для каждого поставщика.")
		} else {
			message := "Не удалось прочитать Excel-файлы: файл повреждён или структура листов и колонок не поддерживается. Проверьте файлы и повторите загрузку."
			var validation *workbook.ValidationError
			if errors.As(err, &validation) {
				message = validation.Message
			}
			problem(w, 422, "invalid_workbook", message)
		}
		return
	}
	if err := data.Validate(); err != nil {
		a.logger.Warn("validate imported Excel dataset", "error", err)
		problem(w, 422, "invalid_dataset", "Данные в Excel-файлах не прошли проверку. Проверьте значения и связи товаров в комплекте поставщика.")
		return
	}
	// Do not preview a dataset that the regular JSON save endpoint cannot accept.
	body, err := json.Marshal(struct {
		Data planning.Dataset `json:"data"`
	}{data})
	if err != nil {
		problem(w, 500, "conversion_error", "Не удалось подготовить результат Excel-импорта.")
		return
	}
	if len(body) > maxBodyBytes {
		problem(w, 413, "dataset_too_large", "Результат импорта превышает 64 МиБ. Сократите период истории продаж.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

func importReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		problem(w, 413, "body_too_large", "Общий размер загрузки превышает 64 МиБ.")
		return
	}
	problem(w, 400, "invalid_upload", "Не удалось прочитать загрузку Excel. Выберите файлы снова.")
}
