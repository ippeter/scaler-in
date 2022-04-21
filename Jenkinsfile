 pipeline {
  environment {
    swrImage = "swr.ru-moscow-1.hc.sbercloud.ru/cloud-devops/scaler-in"
    swrCredentials = "SWR_Credentials"
    telegramBotID = "Telegram_Bot_ID"
    majorVersion = "1.7"
  }
  
  agent any
  
  stages {
    
    stage('Build Application') {
      steps {
        script {
          sh '''
            #!/bin/bash
            echo "Building version $majorVersion.$BUILD_NUMBER"
            export PATH=$PATH:/usr/local/go/bin
            GOOS=linux go build -o ./app . 
         '''
        }
      }
    }

    stage('Build Image') {
      steps {
        script {
          dockerImage = docker.build(swrImage)
        }
      }
    }
   
    stage('Push Image to SWR') {
      steps {
        script {
          docker.withRegistry('https://swr.ru-moscow-1.hc.sbercloud.ru', swrCredentials) {
            dockerImage.push("$majorVersion.$BUILD_NUMBER")
            dockerImage.push("latest")
          }
        }
      }
    }

    stage('Deploy Image to CCE') {
      steps {
        script {
          sh '''
            #!/bin/bash
            if kubectl get deployment | grep -q scaler-in
            then
              echo "Deployment found, updating..."
              kubectl set image deployment/scaler-in scaler-in="$swrImage:$majorVersion.$BUILD_NUMBER"
            else
              echo "Deployment not found Exiting..."
            fi
          '''
        }
      }
    }
    
    stage('Send Confirmation to Telegram') {
      steps {
        withCredentials([string(credentialsId: telegramBotID, variable: 'strBotID')]) {
          sh '''
          #!/bin/bash
          MESSAGE="Image $swrImage:$majorVersion.$BUILD_NUMBER deployed"
          curl -H "Content-Type: application/json" -X POST https://api.telegram.org/bot${strBotID}/sendMessage -d \'{"chat_id": 455547475, "text": "'"$MESSAGE"'", "parse_mode": "HTML"}\'
          '''        
        }
      }
    } 

  }
}
