package ping

import (
	"fmt"
		"log"
			"net"
				"time"

					"golang.org/x/net/icmp"
						"golang.org/x/net/ipv4"
							"ping-monitor/models"
							)

							const (
								protocolICMP = 1
								)

								// PingServer отправляет ICMP-запрос и проверяет ответ
								func PingServer(server *models.Server, timeout time.Duration) bool {
									// Создаем временный ID и Seq для этого запроса
										id := int(time.Now().UnixNano() % 0xffff)
											seq := int(time.Now().UnixNano() % 0xffff)

												conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
													if err != nil {
															log.Printf("Error listening for ICMP packets: %v", err)
																	return false
																		}
																			defer conn.Close()

																				// Устанавливаем таймаут для соединения
																					err = conn.SetDeadline(time.Now().Add(timeout))
																						if err != nil {
																								log.Printf("Error setting deadline: %v", err)
																										return false
																											}

																												// Создаем ICMP сообщение
																													msg := icmp.Message{
																															Type: ipv4.ICMPTypeEcho,
																																	Code: 0,
																																			Body: &icmp.Echo{
																																						ID:   id,
																																									Seq:  seq,
																																												Data: []byte("HELLO"),
																																														},
																																															}

																																																msgBytes, err := msg.Marshal(nil)
																																																	if err != nil {
																																																			log.Printf("Error marshaling ICMP message: %v", err)
																																																					return false
																																																						}

																																																							dst, err := net.ResolveIPAddr("ip4", server.IP)
																																																								if err != nil {
																																																										log.Printf("Error resolving IP address: %v", err)
																																																												return false
																																																													}

																																																														start := time.Now()
																																																															n, err := conn.WriteTo(msgBytes, dst)
																																																																if err != nil {
																																																																		log.Printf("Error sending ICMP packet to %s: %v", server.IP, err)
																																																																				return false
																																																																					}
																																																																						if n != len(msgBytes) {
																																																																								log.Printf("Incomplete ICMP packet sent to %s", server.IP)
																																																																										return false
																																																																											}

																																																																												// Читаем ответ
																																																																													reply := make([]byte, 1500)
																																																																														n, peer, err := conn.ReadFrom(reply)
																																																																															if err != nil {
																																																																																	log.Printf("Error reading ICMP reply from %s: %v", server.IP, err)
																																																																																			return false
																																																																																				}

																																																																																					duration := time.Since(start)

																																																																																						// Парсим ответ
																																																																																							replyMsg, err := icmp.ParseMessage(protocolICMP, reply[:n])
																																																																																								if err != nil {
																																																																																										log.Printf("Error parsing ICMP reply from %s: %v", server.IP, err)
																																																																																												return false
																																																																																													}

																																																																																														// Проверяем что это ответ на наш запрос
																																																																																															switch replyMsg.Type {
																																																																																																case ipv4.ICMPTypeEchoReply:
																																																																																																		if echoReply, ok := replyMsg.Body.(*icmp.Echo); ok {
																																																																																																					if echoReply.ID == id && echoReply.Seq == seq {
																																																																																																									log.Printf("Server %s (%s) - PING SUCCESS (%.2fms)", 
																																																																																																														server.Name, server.IP, float64(duration.Microseconds())/1000)
																																																																																																																		return true
																																																																																																																					}
																																																																																																																								log.Printf("Server %s (%s) - Invalid ICMP reply: id=%d seq=%d (expected id=%d seq=%d) ip=%s",
																																																																																																																												server.Name, server.IP, echoReply.ID, echoReply.Seq, id, seq, peer.String())
																																																																																																																														}
																																																																																																																															case ipv4.ICMPTypeDestinationUnreachable:
																																																																																																																																	log.Printf("Server %s (%s) - Destination unreachable", server.Name, server.IP)
																																																																																																																																		default:
																																																																																																																																				log.Printf("Server %s (%s) - Unexpected ICMP type: %v", server.Name, server.IP, replyMsg.Type)
																																																																																																																																					}

																																																																																																																																						return false
																																																																																																																																						}