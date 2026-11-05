# Volkolak

Разбор видеоархива: распаковка, кадры, текст на кадре, описание сцены и расшифровка звука.

Инфраструктура и activities работают на сервере в Docker. На ноутбуке запускается только клиент: он принимает архив и исполняет воркфлоу Temporal. Сами файлы через воркфлоу не ходят — в истории лежат ключи объектов в бакете и текстовые результаты.

## Как это разложено

| Где | Процесс | Очередь Temporal | Задача |
|---|---|---|---|
| Ноутбук | `go run ./cmd/api` | `volkolak-workflow` | Swagger и код воркфлоу |
| Сервер | `worker` | `volkolak-activity` | Распаковка, звук, кадры, OCR, запись в Postgres |
| Сервер | `scene-worker` | `volkolak-scene` | Описание кадра через Ollama |
| Сервер | `transcribe-worker` | `volkolak-transcribe` | Расшифровка через whisper на GPU |

Рядом на сервере поднимаются Postgres, Temporal и его UI, хранилище S3 и Ollama. При первом старте создаётся namespace `default`, скачиваются модель `qwen2.5vl:3b` и `ggml-small.bin`.

Образ `minio/minio` с Docker Hub больше не скачивается. Вместо него стоит совместимая сборка Silo (`pgsty/silo`): те же переменные `MINIO_*`, команда `server` и клиент `mc`. Бакет по умолчанию — `volkolak`.

Видеокарта нужна контейнерам `ollama` и `transcribe-worker`. Сборка whisper идёт под RTX 40xx (`sm_89`) и RTX 50xx (`sm_120`), CUDA 12.8. Карту AMD `gpus: all` не подключает.

На Windows 11 с Docker Desktop и WSL2 поставь драйвер NVIDIA в Windows, затем `wsl --update` и `wsl --shutdown`. В Docker Desktop включи движок WSL 2. Linux-драйвер и NVIDIA Container Toolkit внутрь WSL ставить не нужно. Проверка:

```bash
docker run --rm --gpus all nvidia/cuda:12.8.1-base-ubuntu22.04 nvidia-smi
```

## Сервер

Из корня репозитория, где лежит `.env`:

```bash
docker compose up -d --build
```

Первый запуск долгий. Whisper собирается с CUDA в один поток nvcc, чтобы уложиться в 32 ГБ RAM. Затем скачиваются `ggml-small` (~465 МБ) и `qwen2.5vl:3b` (~3.2 ГБ). Воркеры стартуют после контейнера `temporal-create-namespace`: он дожидается Temporal и создаёт namespace `default`.

Воркеры ходят в Temporal по IPv4-адресу сервиса `temporal`. Статический Go-бинарник сам резолвит это имя дольше таймаута клиента.

После старта:

- Temporal UI: `http://<сервер>:8080`
- S3 API: `http://<сервер>:9000`
- Консоль хранилища: `http://<сервер>:9001` (`minio` / `minioadmin`)

Postgres и Ollama опубликованы только на `127.0.0.1` сервера. Temporal (`7233`), UI и S3 слушают все интерфейсы.

Остановить и удалить тома (базу, модели, бакет):

```bash
docker compose down -v
```

## Ноутбук

Нужен Go 1.26. Скопируй пример и поставь адрес сервера:

```bash
cp .env.local.example .env.local
```

В `.env.local` замени `127.0.0.1` на IP сервера в `TEMPORAL_ADDRESS` и `S3_ENDPOINT`. Имена очередей должны совпадать с серверным `.env`.

```bash
set -a && source .env.local && set +a && go run ./cmd/api
```

Swagger: http://localhost:8088/swagger/

`POST /v1/archives`, поле `file`. Ответ `202` сразу содержит `workflow_id`, `run_id` и ключ архива. Дальше обработка идёт сама. Ход смотри в Temporal UI, результат — в Postgres.

Поддерживаются zip, tar, tar.gz, gzip, bzip2, tar.bz2, 7z и xz. RAR определяется, но не распаковывается.

## Что происходит с одним архивом

1. Клиент кладёт архив в бакет `volkolak` и стартует `ArchiveWorkflow`.
2. Сервер определяет тип, распаковывает и оставляет только видео.
3. На каждый ролик запускается дочерний `VideoWorkflow`.
4. В базу пишутся имя файла и uuid.
5. Из ролика достаётся WAV и кадры. JPEG сохраняется с шириной не больше 1280. По умолчанию кадр раз в 2 секунды и не больше 30 на ролик. Если видео длинное, шаг растягивается на всю длительность.
6. Tesseract (`rus+eng`) читает текст на кадре. Ollama описывает сценарий по уменьшенной копии (длинная сторона 768). Кадр без описания не останавливает ролик: причина пишется в `frame_analyses.error`.
7. Whisper расшифровывает звук на GPU.
8. Текст кадров и расшифровка сохраняются в Postgres.

Кадры и звук после шага удаляются с диска контейнера. В бакете остаются архив, видео, WAV и JPEG.

Число кадров меняется на сервере: `MAX_FRAMES` в `.env`, затем `docker compose up -d worker`. Значение в `.env.local` на нарезку не влияет.

## Где смотреть результат

На сервере:

```bash
docker compose exec postgres psql -U temporal -d volkolak
```

```sql
SELECT id, original_filename, object_key, status FROM videos ORDER BY created_at;

SELECT v.original_filename, f.frame_index, f.timestamp_sec, f.ocr_text, f.description, f.error
FROM frame_analyses f
JOIN videos v ON v.id = f.video_id
ORDER BY v.created_at, f.frame_index;

SELECT v.original_filename, t.language, t.text
FROM transcripts t
JOIN videos v ON v.id = t.video_id;
```

`status`: `registered` — файл записан, `analyzed` — кадры разобраны, `done` — есть расшифровка.

Длинный текст удобнее так: `\x`, затем запрос. Выход: `\q`.

## Настройки

Сервер читает `.env`. Ноутбук читает переменные окружения, пример — `.env.local.example`.

| Переменная | Зачем |
|---|---|
| `TEMPORAL_ADDRESS` | Куда подключаться. На сервере это `temporal:7233`, на ноутбуке — `IP:7233` |
| `TEMPORAL_PORT`, `TEMPORAL_UI_PORT` | Порты gRPC и UI на сервере, по умолчанию `7233` и `8080` |
| `TEMPORAL_*_TASK_QUEUE` | Четыре очереди. На обеих сторонах имена должны совпадать |
| `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET` | Бакет Silo |
| `FRAME_INTERVAL_SEC` | Базовый интервал между кадрами, секунды (1–3600). Читает серверный `worker` |
| `MAX_FRAMES` | Потолок кадров с одного видео (1–300). Читает серверный `worker` |
| `VISION_MODEL` | Модель Ollama. Легче по памяти: `moondream` |
| `WHISPER_MODEL_NAME` | Имя ggml-модели, по умолчанию `small` |
| `HTTP_ADDR` | Адрес локального API, по умолчанию `:8088` |
| `HTTP_MAX_UPLOAD_MB` | Предел размера архива |
| `WORKFLOW_TIMEOUT_HOURS` | Предел на один архив |
