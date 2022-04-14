 pipeline {
  environment {
    registry = "ippeter/scaler-in"
  }
  
  agent any
  
  stages {
   
    stage('Build Application') {
      steps {
        script {
          sh '''#!/bin/bash
                GOOS=linux go build -o ./app . 
         '''
        }
      }
    }

    stage('Build Image') {
      steps {
        script {
          dockerImage = docker.build(registry + ":1.4.${env.BUILD_NUMBER}")
        }
      }
    }
    
    /*
    stage('Push Image') {
      steps {
        script {
          docker.withRegistry( '', registryCredential ) {
            dockerImage.push()
          }
        }
      }
    }
    */

    /*
    stage('Deploy Image') {
      steps {
        script {
          withAWS(region:'us-west-2', credentials:'aws-final') {
            sh '''
              if kubectl get deployment | grep -q mysql-tester
              then
                echo "Deployment found, updating..."
                kubectl set image deployment/mysql-tester mysql-tester="$registry:$BUILD_NUMBER"
              else
                echo "Deployment not found, creating for the first time and exposing..."
                kubectl create deployment mysql-tester --image="$registry:$BUILD_NUMBER"
                kubectl scale deployment mysql-tester --replicas=2
                kubectl expose deployment mysql-tester  --type=LoadBalancer --port=8081 --target-port=5000
              fi
            '''
          }
        }
      }
    }
    */
    
    /*
    stage('Create External Service') {
      steps {
        withAWS(region:'us-west-2', credentials:'aws-final') {
          sh 'kubectl create -f /capstone/external-service.yaml'
        }
      }
    } 
    */
    
    /*
    stage('Get RDS Endpoint') {
      steps {
        withAWS(region:'us-west-2', credentials:'aws-final') {
          sh 'aws cloudformation describe-stacks --stack-name rds --query Stacks[0].Outputs[0].OutputValue > /tmp/rds-endpoint.txt'
        }
      }
    } 
    */

  }
}
