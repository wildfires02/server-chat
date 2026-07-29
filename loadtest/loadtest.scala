package tinode

import java.util.Base64
import java.util.concurrent.ConcurrentHashMap

import scala.collection.JavaConverters._
import scala.collection._
import scala.concurrent.duration._

import io.gatling.core.Predef._
import io.gatling.http.Predef._

/** Loadtest 使用账户数据随机登录并遍历用户已有订阅进行综合压测。 */
class Loadtest extends TinodeBase {
  // 输入文件可通过 "accounts" Java 选项设置。
  // 例如：JAVA_OPTS="-Daccounts=/tmp/z.csv" gatling.sh -sf . -rsf . -rd "na" -s tinode.Loadtest
  val feeder = csv(System.getProperty("accounts", "users.csv")).random

  // scn 定义登录、同步订阅、发布和离开 Topic 的完整 WebSocket 场景。
  val scn = scenario("WebSocket")
    .exec(ws("Connect WS").connect("/v0/channels?apikey=AQEAAAABAAD_rAp4DJh05a1HAwFT3A6K"))
    .exec(session => session.set("id", "tn-" + session.userId))
    .pause(1)
    .exec(hello)
    .pause(1)
    .feed(feeder)
    .doIfOrElse({session =>
      val uname = session("username").as[String]
      var token = session("token").asOption[String]
      if (token == None) {
        token = tokenCache.get(uname)
      }
      token == None
    }) { loginBasic } { loginToken }
    .exitHereIfFailed
    .exec(subMe)
    .exitHereIfFailed
    .exec(getSubs)
    .exitHereIfFailed
    .doIf({session =>
      session.attributes.contains("subs")
    }) {
      exec { session =>
        // 打乱订阅顺序。
        val subs = session("subs").as[Vector[String]]
        val shuffled = scala.util.Random.shuffle(subs.toList)
        session.set("subs", shuffled)
      }
      .foreach("${subs}", "sub") {
        exec(subTopic)
        .exitHereIfFailed
        .pause(0, 2)
        .doIfOrElse({session =>
          val topic = session("sub").as[String]
          !topic.startsWith("chn")
        }) { publish } { pause(5) }
        .exec(leaveTopic)
        .pause(0, 3)
      }
    }
    .exec(ws("close-ws").close)

  setUp(scn.inject(rampUsers(numSessions) during (rampPeriod.seconds))).protocols(httpProtocol)
}

/** MeLoadtest 针对单一账号的 me Topic 执行长连接稳定性压测。 */
class MeLoadtest extends TinodeBase {
  // username 是单账号压测使用的登录名。
  val username = System.getProperty("username", "user0")
  // password 是单账号压测使用的密码。
  val password = System.getProperty("password", "user0123")

  // scn 定义单账号登录、同步与长时间保持连接的场景。
  val scn = scenario("WebSocket")
    .exec(ws("Connect WS").connect("/v0/channels?apikey=AQEAAAABAAD_rAp4DJh05a1HAwFT3A6K"))
    .exec(session => session.set("id", "tn-" + session.userId))
    .exec(session => session.set("username", username))
    .exec(session => session.set("password", password))
    .pause(1)
    .exec(hello)
    .pause(1)
    .doIfOrElse({session =>
      val uname = session("username").as[String]
      val token = tokenCache.get(username)
      token == None
    }) { loginBasic } { loginToken }
    .exitHereIfFailed
    .exec(subMe)
    .exitHereIfFailed
    .exec(getSubs)
    .exitHereIfFailed
    .pause(1000)
    .exec(ws("close-ws").close)

  setUp(scn.inject(rampUsers(numSessions) during (rampPeriod.seconds))).protocols(httpProtocol)
}

/** SingleTopicLoadtest 让多用户集中订阅并发布到同一个指定 Topic。 */
class SingleTopicLoadtest extends TinodeBase {
  // 输入文件可通过 "accounts" Java 选项设置。
  // 例如：JAVA_OPTS="-Daccounts=/tmp/z.csv" gatling.sh -sf . -rsf . -rd "na" -s tinode.Loadtest
  val feeder = csv(System.getProperty("accounts", "users.csv")).random
  // topic 是所有压测用户共同访问的目标 Topic。
  val topic = System.getProperty("topic", "TOPIC_NAME")

  // scn 定义多用户登录、订阅指定 Topic、发布和离开的场景。
  val scn = scenario("WebSocket")
    .exec(ws("Connect WS").connect("/v0/channels?apikey=AQEAAAABAAD_rAp4DJh05a1HAwFT3A6K"))
    .exec(session => session.set("id", "tn-" + session.userId))
    .pause(1)
    .exec(hello)
    .pause(1)
    .feed(feeder)
    .doIfOrElse({session =>
      val uname = session("username").as[String]
      var token = session("token").asOption[String]
      if (token == None) {
        token = tokenCache.get(uname)
      }
      token == None
    }) { loginBasic } { loginToken }
    .exitHereIfFailed
    .exec(subMe)
    .exitHereIfFailed
    .exec(getSubs)
    .exitHereIfFailed
    .doIf({session =>
      session.attributes.contains("subs")
    }) {
      exec(session => session.set("sub", topic))
      .exec(subTopic)
      .exitHereIfFailed
      .pause(0, 10)
      .exec(publish)
      .pause(15)
      .exec(leaveTopic)
      .pause(0, 3)
    }
    .exec(ws("close-ws").close)

  setUp(scn.inject(rampUsers(numSessions) during (rampPeriod.seconds))).protocols(httpProtocol)
}
