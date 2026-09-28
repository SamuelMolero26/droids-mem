// Command api is the top of the e2e stress chain:
// main -> Run -> HandleLogin -> auth.Login. HandleGet exercises a second
// entry edge into store.Get.
package main

import (
	"fmt"

	"e2e-fixture/internal/auth"
	"e2e-fixture/internal/store"
)

func main() {
	Run()
}

func Run() {
	fmt.Println(HandleLogin())
	fmt.Println(HandleGet())
}

func HandleLogin() string {
	svc := auth.Service{Store: store.DB{}}
	return svc.Login()
}

func HandleGet() string {
	return store.DB{}.Get("health")
}
