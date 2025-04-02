package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Person описывает сущность человека с обогащёнными данными.
type Person struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `json:"name"`
	Surname     string `json:"surname"`
	Patronymic  string `json:"patronymic,omitempty"`
	Age         int    `json:"age"`
	Gender      string `json:"gender"`
	Nationality string `json:"nationality"`
}

var db *gorm.DB

// initDB устанавливает соединение с базой данных.
func initDB() {
	dsn := os.Getenv("DATABASE_URL")
	var err error
	db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Ошибка подключения к БД: %v", err)
	}
	if err = db.AutoMigrate(&Person{}); err != nil {
		log.Fatalf("Ошибка миграции: %v", err)
	}
	log.Println("БД успешно инициализирована и мигрирована")
}

// enrichData получает данные из открытых API по переданному имени.
func enrichData(name string) (age int, gender string, nationality string, err error) {
	// Получение возраста из agify.io
	resp, err := http.Get("https://api.agify.io/?name=" + name)
	if err != nil {
		log.Printf("Ошибка вызова agify: %v", err)
		return
	}
	defer resp.Body.Close()
	var agifyResp struct {
		Age int `json:"age"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&agifyResp); err != nil {
		log.Printf("Ошибка декодирования ответа agify: %v", err)
		return
	}
	age = agifyResp.Age

	// Получение пола из genderize.io
	resp, err = http.Get("https://api.genderize.io/?name=" + name)
	if err != nil {
		log.Printf("Ошибка вызова genderize: %v", err)
		return
	}
	defer resp.Body.Close()
	var genderizeResp struct {
		Gender string `json:"gender"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&genderizeResp); err != nil {
		log.Printf("Ошибка декодирования ответа genderize: %v", err)
		return
	}
	gender = genderizeResp.Gender

	// Получение национальности из nationalize.io
	resp, err = http.Get("https://api.nationalize.io/?name=" + name)
	if err != nil {
		log.Printf("Ошибка вызова nationalize: %v", err)
		return
	}
	defer resp.Body.Close()
	var nationalizeResp struct {
		Country []struct {
			CountryID   string  `json:"country_id"`
			Probability float64 `json:"probability"`
		} `json:"country"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&nationalizeResp); err != nil {
		log.Printf("Ошибка декодирования ответа nationalize: %v", err)
		return
	}
	if len(nationalizeResp.Country) > 0 {
		nationality = nationalizeResp.Country[0].CountryID
	}
	log.Printf("Данные обогащены для имени %s: возраст=%d, пол=%s, национальность=%s", name, age, gender, nationality)
	return
}

// createPerson обрабатывает POST запрос на добавление нового человека.
func createPerson(c *gin.Context) {
	var input struct {
		Name       string `json:"name" binding:"required"`
		Surname    string `json:"surname" binding:"required"`
		Patronymic string `json:"patronymic"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		log.Printf("Ошибка валидации данных: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	age, gender, nationality, err := enrichData(input.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка обогащения данных"})
		return
	}
	person := Person{
		Name:        input.Name,
		Surname:     input.Surname,
		Patronymic:  input.Patronymic,
		Age:         age,
		Gender:      gender,
		Nationality: nationality,
	}
	if err = db.Create(&person).Error; err != nil {
		log.Printf("Ошибка сохранения в БД: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("Создан новый человек: %v", person)
	c.JSON(http.StatusOK, person)
}

// getPersons возвращает список людей с фильтрами и пагинацией.
func getPersons(c *gin.Context) {
	var persons []Person
	query := db

	// Фильтрация по имени (регистронезависимый поиск)
	if name := c.Query("name"); name != "" {
		query = query.Where("name ILIKE ?", "%"+name+"%")
	}

	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		page = 1
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if err != nil || limit < 1 {
		limit = 10
	}
	offset := (page - 1) * limit
	query = query.Offset(offset).Limit(limit)

	if err := query.Find(&persons).Error; err != nil {
		log.Printf("Ошибка получения данных из БД: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, persons)
}

// updatePerson обрабатывает PUT запрос на обновление данных человека.
func updatePerson(c *gin.Context) {
	id := c.Param("id")
	var person Person
	if err := db.First(&person, id).Error; err != nil {
		log.Printf("Человек с id %s не найден: %v", id, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Человек не найден"})
		return
	}

	var input struct {
		Name       string `json:"name"`
		Surname    string `json:"surname"`
		Patronymic string `json:"patronymic"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		log.Printf("Ошибка валидации данных при обновлении: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Если имя изменилось, повторно обогащаем данные
	if input.Name != "" && input.Name != person.Name {
		age, gender, nationality, err := enrichData(input.Name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка обогащения данных"})
			return
		}
		person.Age = age
		person.Gender = gender
		person.Nationality = nationality
		person.Name = input.Name
	}
	if input.Surname != "" {
		person.Surname = input.Surname
	}
	if input.Patronymic != "" {
		person.Patronymic = input.Patronymic
	}

	if err := db.Save(&person).Error; err != nil {
		log.Printf("Ошибка обновления данных в БД: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("Данные обновлены для человека id %s", id)
	c.JSON(http.StatusOK, person)
}

// deletePerson обрабатывает DELETE запрос для удаления человека по id.
func deletePerson(c *gin.Context) {
	id := c.Param("id")
	if err := db.Delete(&Person{}, id).Error; err != nil {
		log.Printf("Ошибка удаления записи с id %s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("Запись с id %s успешно удалена", id)
	c.JSON(http.StatusOK, gin.H{"message": "Удалено"})
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Не удалось загрузить .env файл, используем системные переменные")
	}

	initDB()

	router := gin.Default()

	// Роуты REST API
	router.POST("/persons", createPerson)
	router.GET("/persons", getPersons)
	router.PUT("/persons/:id", updatePerson)
	router.DELETE("/persons/:id", deletePerson)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Сервис запущен на порту %s", port)
	router.Run(":" + port)
}

