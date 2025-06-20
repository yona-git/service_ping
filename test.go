### Проблема в текущей реализации:
Из ваших логов видно, что **все серверы получают ответы с ID=3 и Seq=797**, хотя ожидают разные значения. Это приводит к тому, что:
1. Каждый сервер считает эти ответы "неправильными"
2. Все серверы продолжают попытки пинга, хотя ответ уже пришел
3. Реальные ответы игнорируются, так как не совпадают с ожидаемыми ID/Seq

### Корень проблемы:
1. **Сетевой уровень**: Что-то (фаервол/NAT) перехватывает и перезаписывает ICMP-пакеты, подставляя свои ID и Seq
2. **Логика проверки**: Текущий код слишком строго проверяет соответствие ID/Seq

### Полное решение:

```go
package ping

import (
    	"context"
        	"log"
            	"net"
                	"sync"
                    	"sync/atomic"
                        	"time"

                            	"golang.org/x/net/icmp"
                                	"golang.org/x/net/ipv4"
)

const (
    	maxPingAttempts    = 5
        	pingInterval       = 5 * time.Second
            	initialRetryDelay  = 100 * time.Millisecond
                	maxRetryDelay      = 500 * time.Millisecond
                    	responseTimeout    = 2 * time.Second
                        	logPrefix          = "PING: "
                            	expectedReplyCount = 1 // Сколько ответов считать успехом
)

type PingSession struct {
    	Server       *models.Server
        	IP           net.IP
            	CurrentID    uint16
                	CurrentSeq   int
                    	ReplyCounter int
                        	LastReply    time.Time
}

var (
    	idCounter  uint32
        	logMu      sync.Mutex
            	globalConn *icmp.PacketConn
                	connOnce   sync.Once
                    	sessions   sync.Map // map[net.IP]*PingSession
)

func getNextID() uint16 {
    	return uint16(atomic.AddUint32(&idCounter, 1) & 0xffff
}

func getGlobalConn() (*icmp.PacketConn, error) {
    	var err error
        	connOnce.Do(func() {
                		globalConn, err = icmp.ListenPacket("ip4:icmp", "0.0.0.0")
                        		if err == nil {
                                    			go readResponses(globalConn)
                                }
            })
            	return globalConn, err
}

func readResponses(conn *icmp.PacketConn) {
    	rb := make([]byte, 1500)
        	for {
                		n, peer, err := conn.ReadFrom(rb)
                        		if err != nil {
                                    			if netErr, ok := err.(net.Error); ok && !netErr.Temporary() {
                                                    				break
                                                }
                                                			continue
                                }

                                		peerIP, _, err := net.SplitHostPort(peer.String())
                                        		if err != nil {
                                                    			continue
                                                }

                                                		rm, err := icmp.ParseMessage(ipv4.ICMPTypeEchoReply.Protocol(), rb[:n])
                                                        		if err != nil || rm.Type != ipv4.ICMPTypeEchoReply {
                                                                    			continue
                                                                }

                                                                		echoReply, ok := rm.Body.(*icmp.Echo)
                                                                        		if !ok {
                                                                                    			continue
                                                                                }

                                                                                		// Ищем сессию по IP (а не по ID!)
                                                                                        		if session, ok := sessions.Load(net.ParseIP(peerIP)); ok {
                                                                                                    			s := session.(*PingSession)
                                                                                                                			s.LastReply = time.Now()
                                                                                                                            			s.ReplyCounter++
                                                                                                                                        			logServer(s.Server, "Got reply from %s (ID=%d Seq=%d Count=%d)", 
                                                                                                                                                    				peerIP, echoReply.ID, echoReply.Seq, s.ReplyCounter)
                                                                                                }
            }
}

func MonitorServers(servers []*models.Server, ctx context.Context) {
    	conn, err := getGlobalConn()
        	if err != nil {
                		log.Printf("%sFailed to initialize ICMP listener: %v", logPrefix, err)
                        		return
            }

            	var wg sync.WaitGroup
                	for _, server := range servers {
                        		if server.IP == "" {
                                    			continue
                                }

                                		wg.Add(1)
                                        		go func(s *models.Server) {
                                                    			defer wg.Done()
                                                                			monitorSingleServer(conn, s, ctx)
                                                }(server)
                    }
                    	wg.Wait()
}

func monitorSingleServer(conn *icmp.PacketConn, server *models.Server, ctx context.Context) {
    	ipAddr, err := net.ResolveIPAddr("ip4", server.IP)
        	if err != nil {
                		logServer(server, "Resolve error: %v", err)
                        		return
            }

            	session := &PingSession{
                    		Server:     server,
                            		IP:         ipAddr.IP,
                                    		CurrentID:  getNextID(),
                                            		CurrentSeq: rand.Intn(1000),
                }
                	sessions.Store(ipAddr.IP, session)
                    	defer sessions.Delete(ipAddr.IP)

                        	ticker := time.NewTicker(pingInterval)
                            	defer ticker.Stop()

                                	for {
                                        		select {
                                                    		case <-ctx.Done():
                                                            			logServer(server, "Monitoring stopped")
                                                                        			return
                                                                                    		case <-ticker.C:
                                                                                            			if checkServer(conn, session) {
                                                                                                            				updateServerStatus(server, true)
                                                                                                        } else {
                                                                                                            				updateServerStatus(server, false)
                                                                                                        }
                                                                                                        			// Сброс счетчика для следующей проверки
                                                                                                                    			session.ReplyCounter = 0
                                                }
                                    }
}

func checkServer(conn *icmp.PacketConn, session *PingSession) bool {
    	retryDelay := initialRetryDelay
        	timeout := time.After(responseTimeout)

            	for attempt := 0; attempt < maxPingAttempts; attempt++ {
                    		session.CurrentSeq++
                            		logServer(session.Server, "Attempt %d/%d (ID: %d, Seq: %d)", 
                                    			attempt+1, maxPingAttempts, session.CurrentID, session.CurrentSeq)

                                                		if err := sendPing(conn, session); err != nil {
                                                            			logServer(session.Server, "Send error: %v", err)
                                                                        			continue
                                                        }

                                                        		select {
                                                                    		case <-time.After(retryDelay):
                                                                            			// Увеличиваем задержку между попытками
                                                                                        			retryDelay = min(retryDelay*2, maxRetryDelay)
                                                                                                    		case <-timeout:
                                                                                                            			return session.ReplyCounter >= expectedReplyCount
                                                                }

                                                                		if session.ReplyCounter >= expectedReplyCount {
                                                                            			return true
                                                                        }
                }

                	return session.ReplyCounter >= expectedReplyCount
}

func sendPing(conn *icmp.PacketConn, session *PingSession) error {
    	wm := icmp.Message{
            		Type: ipv4.ICMPTypeEcho,
                    		Code: 0,
                            		Body: &icmp.Echo{
                                        			ID:   int(session.CurrentID),
                                                    			Seq:  session.CurrentSeq,
                                                                			Data: []byte("HELLO"),
                                    },
        }

        	wb, err := wm.Marshal(nil)
            	if err != nil {
                    		return err
                }

                	_, err = conn.WriteTo(wb, &net.IPAddr{IP: session.IP})
                    	return err
}

func logServer(server *models.Server, format string, args ...interface{}) {
    	logMu.Lock()
        	defer logMu.Unlock()
            	log.Printf("%sServer %s (%s) - "+format,
                		append([]interface{}{logPrefix, server.Name, server.IP}, args...)...)
}

func updateServerStatus(server *models.Server, isAlive bool) {
    	status := "dead"
        	if isAlive {
                		status = "alive"
            }

            	if server.Status != status {
                    		server.Status = status
                            		logServer(server, "Status changed to %s", status)
                }
}
```

### Ключевые изменения:

1. **Сессии вместо строгих проверок**:
   - Храним активные сессии по IP-адресу (`sessions sync.Map`)
      - Любой ответ с правильного IP считается валидным

      2. **Новая логика успеха**:
         - Сервер считается "живым", если получили ≥1 ответ (`expectedReplyCount`)
            - Не проверяем строго ID/Seq (так как их переписывает сеть)

            3. **Улучшенное управление попытками**:
               - Экспоненциальная backoff-задержка между попытками
                  - Общий таймаут 2 секунды на все попытки

                  4. **Статистика ответов**:
                     - Считаем все ответы от целевого IP
                        - Логируем количество полученных ответов

                        ### Как это работает теперь:

                        1. Для каждого сервера:
                           - Создается сессия с уникальным ID
                              - Отправляется до 5 ping-запросов с увеличивающейся задержкой
                                 - Любой ответ с правильного IP увеличивает счетчик

                                 2. Успех определяется по:
                                    - Получению ≥1 ответа от целевого IP
                                       - В рамках общего таймаута (2 секунды)

                                       3. При получении ответа:
                                          - Ищем сессию по IP (не по ID!)
                                             - Обновляем счетчик ответов
                                                - Логируем факт получения ответа

                                                ### Преимущества нового подхода:

                                                1. **Устойчивость к перезаписи ID/Seq**:
                                                   - Не зависит от того, какие ID/Seq подставляет сеть

                                                   2. **Эффективное использование попыток**:
                                                      - Первый же успешный ответ завершает проверку
                                                         - Не тратятся попытки на уже отвечающие серверы

                                                         3. **Гибкость**:
                                                            - Можно настроить `expectedReplyCount` для разных требований
                                                               - Легко адаптируется под особенности сети

                                                               4. **Четкая логика**:
                                                                  - Ответы четко привязываются к IP-адресу
                                                                     - Нет ложных срабатываний от чужих ответов

                                                                     Это решение должно корректно работать в вашей сети, даже при наличии NAT/firewall, которые модифицируют ICMP-пакеты.
                }
            }
}
}
                }
                                    }
        }
}
                                                                        }
                                                                }
                                                        }
                }
}
                                                                                                        }
                                                                                                        }
                                                }
                                    }
                }
            }
}
                                                }
                                }
                    }
            }
}
                                                                                                }
                                                                                }
                                                                }
                                                }
                                                }
                                }
            }
}
                                }
            })
})
}
)
}
)
)