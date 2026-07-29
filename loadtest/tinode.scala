package tinode

import java.util.Base64
import java.util.concurrent.ConcurrentHashMap

import scala.collection.JavaConverters._
import scala.collection._
import scala.concurrent.duration._

import io.gatling.core.Predef._
import io.gatling.http.Predef._

/** TinodeBase 定义所有 Gatling WebSocket 压测场景复用的协议和请求步骤。 */
class TinodeBase extends Simulation {
  // 配置服务端 HTTP 与 WebSocket 基础地址。
  val httpProtocol = http
    .baseUrl("http://localhost:6060")
    .wsBaseUrl("ws://localhost:6060")

  // 用于会话间共享的认证令牌缓存。
  val tokenCache : concurrent.Map[String, String] = new ConcurrentHashMap() asScala
  // 发布到主题的总消息数。
  val publishCount = Integer.getInteger("publish_count", 10).toInt
  // 发布消息到主题的最大间隔时间。
  val publishInterval = Integer.getInteger("publish_interval", 100).toInt
  // 总会话数。
  val numSessions = Integer.getInteger("num_sessions", 10000)
  // 用户递增时间窗口（从 0 到 numSessions），单位：秒。
  val rampPeriod = java.lang.Long.getLong("ramp", 300L)

  // hello 建立协议握手并等待服务端控制响应。
  val hello = exitBlockOnFail {
    exec {
      ws("hi").sendText(
        """{"hi":{"id":"afabb3","ver":"0.22.8","ua":"Gatling-Loadtest/1.0; gatling/1.7.0"}}"""
      )
      .await(15 seconds)(
        ws.checkTextMessage("hi")
          .matching(jsonPath("$.ctrl").find.exists)
      )
    }
  }

  // loginBasic 使用用户名和密码登录，并缓存服务端签发的令牌。
  val loginBasic = exitBlockOnFail {
    exec { session =>
      val uname = session("username").as[String]
      val password = session("password").as[String]
      val secret = new String(java.util.Base64.getEncoder.encode((uname + ":" + password).getBytes()))
      session.set("secret", secret)
    }
    .exec {
      ws("login").sendText(
        """{"login":{"id":"${id}-login","scheme":"basic","secret":"${secret}"}}"""
      )
      .await(15 seconds)(
        ws.checkTextMessage("login-ctrl")
          .matching(jsonPath("$.ctrl").find.exists)
          .check(jsonPath("$.ctrl.params.token").saveAs("token"))
      )
    }
    .exec { session =>
      val uname = session("username").as[String]
      val token = session("token").as[String]
      tokenCache.put(uname, token)
      session
    }
  }

  // loginToken 使用已缓存的令牌恢复登录状态。
  val loginToken = exitBlockOnFail {
    exec { session =>
      val uname = session("username").as[String]
      var token = session("token").asOption[String]
      if (token == None) {
        token = tokenCache.get(uname)
      }
      session.set("token", token.getOrElse(""))
    }
    .exec {
      ws("login-token").sendText(
        """{"login":{"id":"${id}-login2","scheme":"token","secret":"${token}"}}"""
      )
      .await(15 seconds)(
        ws.checkTextMessage("login-ctrl")
          .matching(jsonPath("$.ctrl").find.exists)
      )
    }
  }

  // subMe 订阅当前用户的 me Topic。
  val subMe = exitBlockOnFail {
    exec {
      ws("sub-me").sendText(
        """{"sub":{"id":"${id}-sub-me","topic":"me","get":{"what":"desc"}}}"""
      )
      .await(15 seconds)(
        ws.checkTextMessage("sub-me-desc")
          .matching(jsonPath("$.ctrl").find.exists)
          .check(jsonPath("$.ctrl.code").ofType[Int].in(200 to 299))
      )
    }
  }

  // subTopic 订阅当前 feeder 选择的会话并拉取初始状态。
  val subTopic = exitBlockOnFail {
    exec {
      ws("sub-topic").sendText(
        """{"sub":{"id":"${id}-sub-${sub}","topic":"${sub}","get":{"what":"desc sub data del"}}}"""
      )
      .await(15 seconds)(
        ws.checkTextMessage("sub-topic-ctrl")
          .matching(jsonPath("$.ctrl").find.exists)
          .check(jsonPath("$.ctrl.code").ofType[Int].in(200 to 299))
      )
    }
  }

  // publish 按配置的次数和间隔向当前 Topic 发布测试消息。
  val publish = exitBlockOnFail {
    exec {
      repeat(publishCount, "i") {
        exec {
          ws("pub-topic").sendText(
            """{"pub":{"id":"${id}-pub-${sub}-${i}","topic":"${sub}","content":"This is a Gatling test ${i}"}}"""
          )
          .await(15 seconds)(
            ws.checkTextMessage("pub-topic-ctrl")
              .matching(jsonPath("$.ctrl").find.exists)
              .check(jsonPath("$.ctrl.code").ofType[Int].in(200 to 299))
          )
        }
        .pause(0, publishInterval)
      }
    }
  }

  // getSubs 拉取当前用户的 Topic 订阅列表并保存到 Gatling 会话。
  val getSubs = exitBlockOnFail {
    exec {
      ws("get-subs").sendText(
        """{"get":{"id":"${id}-get-subs","topic":"me","what":"sub"}}"""
      )
      .await(15 seconds)(
        ws.checkTextMessage("save-subs")
          .matching(jsonPath("$.meta.sub").find.exists)
          .check(jsonPath("$.meta.sub[*].topic").findAll.saveAs("subs"))
      )
    }
  }

  // leaveTopic 离开当前测试 Topic。
  val leaveTopic = exitBlockOnFail {
    exec {
      ws("leave-topic").sendText(
        """{"leave":{"id":"${id}-leave-${sub}","topic":"${sub}"}}"""
      )
      .await(15 seconds)(
        ws.checkTextMessage("sub-topic-ctrl")
          .matching(jsonPath("$.ctrl").find.exists)
      )
    }
  }
}
