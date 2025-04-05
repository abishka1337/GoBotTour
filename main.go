package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/gocolly/colly/v2"
	"github.com/jackc/pgx/v5"
)

const (
	dbConnStr      = "postgresql://neondb_owner:npg_8GJQ4Ogkdoib@ep-lingering-sun-a2wkiat7-pooler.eu-central-1.aws.neon.tech/neondb?sslmode=require"
	botToken       = "8147802242:AAHOGcmj3SW_Yi2degfpWCV01FdZ1m0DoSw" // Заменить на токен
	chatID         = 697804659                                        // Укажи свой Telegram Chat ID
	priceThreshold = 450000
)

// Подключение к базе данных
func connectDB() (*pgx.Conn, error) {
	conn, err := pgx.Connect(context.Background(), dbConnStr)
	if err != nil {
		return nil, fmt.Errorf("Failed to connect to database: %v", err)
	}
	return conn, nil
}

// Вставка нового тура в базу данных и отправка уведомления в Telegram, если цена ниже порога
func insertTour(conn *pgx.Conn, bot *tgbotapi.BotAPI, tourURL string, priceInt int, hotelName, location, options string) error {
	// Проверяем, есть ли уже такой тур в базе
	var exists bool
	err := conn.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM tours WHERE url=$1)", tourURL).Scan(&exists)
	if err != nil {
		return fmt.Errorf("Error checking existing tour: %v", err)
	}

	if !exists {
		_, err = conn.Exec(context.Background(), "INSERT INTO tours (url, price, hotel_name, location, options) VALUES ($1, $2, $3, $4, $5)", tourURL, priceInt, hotelName, location, options)
		if err != nil {
			return fmt.Errorf("Error inserting tour into database: %v", err)
		}

		// Отправляем уведомление, если цена ниже порога
		if priceInt <= priceThreshold {
			msgText := fmt.Sprintf("🔥 Новый тур по низкой цене!\n\n🏨 Отель: %s\n📍 Локация: %s\n💰 Цена: %d KZT\n📝 Опции: %s\n🔗 Ссылка: %s", hotelName, location, priceInt, options, tourURL)
			msg := tgbotapi.NewMessage(chatID, msgText)
			bot.Send(msg)
		}
	}
	return nil
}

// Функция парсинга данных с сайта
func startScraping(c *colly.Collector, conn *pgx.Conn, bot *tgbotapi.BotAPI) {
	c.OnHTML("a.trv-hot-tours-2__item", func(e *colly.HTMLElement) {
		tourURL := e.Attr("href")
		if !strings.HasPrefix(tourURL, "http") {
			tourURL = "https://tourvisor.ru" + tourURL
		}

		// Получаем цену из элемента и очищаем от лишних символов
		price := strings.ReplaceAll(e.ChildText(".trv-hot-tours-2__item-price"), "KZT", "")
		price = strings.ReplaceAll(price, " ", "")

		// Преобразуем строку в целое число
		priceInt, err := strconv.Atoi(price)
		if err != nil {
			log.Printf("Error converting price: %v", err)
			return
		}

		// Получаем другие данные тура
		hotelName := e.ChildText(".trv-hot-tours-2__item-name")
		location := e.ChildText(".trv-hot-tours-2__item-location")

		// Добавляем обработку опций
		options := fmt.Sprintf("Дата вылета: %s, База питания: %s, Кол-во взрослых: %s, Кол-во дней: %s",
			e.ChildText(".trv-hot-tours-2__item-departure"), // Дата вылета
			e.ChildText(".trv-hot-tours-2__item-food"),      // База питания
			e.ChildText(".trv-hot-tours-2__item-adults"),    // Кол-во взрослых
			e.ChildText(".trv-hot-tours-2__item-days"),      // Кол-во дней
		)

		// Передаем в функцию insertTour теперь options, чтобы хранить эти данные
		err = insertTour(conn, bot, tourURL, priceInt, hotelName, location, options)
		if err != nil {
			log.Printf("Error inserting tour: %v", err)
		}
	})

	// Запускаем парсинг страницы
	c.Visit("https://tourvisor.ru/goryashchie-tury?departure=60&country=0&sort=0&nights=0")
}

// Функция для периодического запуска парсинга каждые 10 минут
func periodicScraping(conn *pgx.Conn, bot *tgbotapi.BotAPI) {
	c := colly.NewCollector()
	for {
		log.Println("Запускаем парсинг...")
		startScraping(c, conn, bot)
		log.Println("Ожидание следующего парсинга...")
		time.Sleep(10 * time.Minute)
	}
}

func main() {
	conn, err := connectDB()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(context.Background())

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatal(err)
	}

	// Запуск периодического парсинга в отдельной горутине
	go periodicScraping(conn, bot)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates, err := bot.GetUpdatesChan(u)
	if err != nil {
		log.Fatal(err)
	}

	// Обработчик команд Telegram бота
	for update := range updates {
		if update.Message == nil {
			continue
		}

		switch update.Message.Text {
		case "/start":
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Привет! Я уведомлю тебя, если появятся туры дешевле 450 000 KZT!")
			bot.Send(msg)
		case "/tours":
			// Получаем только туры, где цена <= priceThreshold
			rows, err := conn.Query(context.Background(), "SELECT url, price, hotel_name, location, options FROM tours WHERE price <= $1", priceThreshold)
			if err != nil {
				bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Ошибка при получении туров!"))
				continue
			}
			defer rows.Close()

			// Проверка наличия туров
			if !rows.Next() {
				bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Нет туров, подходящих под ваш запрос."))
				continue
			}

			// Отправка найденных туров
			for rows.Next() {
				var url, hotel, location, options string
				var price int
				err := rows.Scan(&url, &price, &hotel, &location, &options)
				if err != nil {
					log.Printf("Error scanning tour row: %v", err)
					continue
				}
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("🏨 %s\n📍 %s\n💰 %d KZT\n📝 Опции: %s\n🔗 %s", hotel, location, price, options, url))
				bot.Send(msg)
			}
		}
	}
}
